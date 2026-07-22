package anchor

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	gsrpc "github.com/centrifuge/go-substrate-rpc-client/v4"
	"github.com/centrifuge/go-substrate-rpc-client/v4/signature"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types/codec"
	"golang.org/x/crypto/blake2b"
)

// CORD anchors checkpoints into a CORD chain (github.com/dhiway/cord) as
// system.remark extrinsics: the standard digest-anchoring pattern, requiring no
// pallet setup on the target chain. Works against any CORD runtime whose
// signed-extension list matches a known layout (see detectLayout); the layout
// is detected from on-chain metadata at connect time and the adapter refuses
// to run against an unrecognized runtime rather than submit garbage.
type CORD struct {
	RPCURL     string // e.g. ws://cord-dev:9944 or https://weave1.testnet.cord.network
	SURI       string // signer secret URI, e.g. //Alice on a dev chain
	SS58Prefix uint16 // 29 for CORD

	api    *gsrpc.SubstrateAPI
	layout extraLayout
	kp     signature.KeyringPair
}

func (c *CORD) Name() string { return "cord" }

// extraLayout captures how the runtime's SignedExtra differs between CORD
// runtime families. Detected from metadata, never assumed.
type extraLayout struct {
	assetTxPayment bool // ChargeAssetTxPayment (Weave): Option<asset_id> byte in extra
	metadataHash   bool // CheckMetadataHash: mode byte in extra + Option<hash> in additional
}

// knownExtensions are the extension identifiers the adapter understands and
// how each contributes to the encoding. Anything outside this set that carries
// payload bytes makes the runtime unsupported.
var zeroByteExtensions = map[string]bool{
	"CheckNonZeroSender": true, "CheckSpecVersion": true, "CheckTxVersion": true,
	"CheckGenesis": true, "CheckMortality": true, "CheckNonce": true,
	"CheckWeight": true, "WeightReclaim": true, "StorageWeightReclaim": true,
}

func detectLayout(identifiers []string) (extraLayout, error) {
	var l extraLayout
	for _, id := range identifiers {
		switch {
		case zeroByteExtensions[id]:
			// covered by the fixed era/nonce encoding or contributes no bytes
		case id == "ChargeTransactionPayment":
			// plain tip, no extra byte (Loom)
		case id == "ChargeAssetTxPayment":
			l.assetTxPayment = true // tip + Option<asset_id> (Weave)
		case id == "CheckMetadataHash":
			l.metadataHash = true
		default:
			return l, fmt.Errorf("unsupported signed extension %q: refusing to sign for this runtime", id)
		}
	}
	return l, nil
}

// encodeExtra builds the SignedExtra byte string for an immortal, tip-0
// extrinsic under the detected layout.
func encodeExtra(l extraLayout, nonce uint64) ([]byte, error) {
	nonceEnc, err := codec.Encode(types.NewUCompactFromUInt(nonce))
	if err != nil {
		return nil, err
	}
	tipEnc, err := codec.Encode(types.NewUCompactFromUInt(0))
	if err != nil {
		return nil, err
	}
	extra := []byte{0x00} // CheckMortality: immortal era
	extra = append(extra, nonceEnc...)
	extra = append(extra, tipEnc...)
	if l.assetTxPayment {
		extra = append(extra, 0x00) // asset_id = None
	}
	if l.metadataHash {
		extra = append(extra, 0x00) // mode = Disabled
	}
	return extra, nil
}

// encodeAdditional builds the additional-signed payload (never transmitted,
// only signed over) under the detected layout.
func encodeAdditional(l extraLayout, specVersion, txVersion uint32, genesis []byte) ([]byte, error) {
	specEnc, err := codec.Encode(specVersion)
	if err != nil {
		return nil, err
	}
	txEnc, err := codec.Encode(txVersion)
	if err != nil {
		return nil, err
	}
	add := append(specEnc, txEnc...)
	add = append(add, genesis...) // CheckGenesis
	add = append(add, genesis...) // CheckMortality era checkpoint (immortal -> genesis)
	if l.metadataHash {
		add = append(add, 0x00) // Option<metadata hash> = None
	}
	return add, nil
}

func (c *CORD) connect() error {
	if c.api != nil {
		return nil
	}
	api, err := gsrpc.NewSubstrateAPI(c.RPCURL)
	if err != nil {
		return fmt.Errorf("connect %s: %w", c.RPCURL, err)
	}
	meta, err := api.RPC.State.GetMetadataLatest()
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	ids := make([]string, 0, 12)
	for _, se := range meta.AsMetadataV14.Extrinsic.SignedExtensions {
		ids = append(ids, string(se.Identifier))
	}
	layout, err := detectLayout(ids)
	if err != nil {
		return err
	}
	kp, err := signature.KeyringPairFromSecret(c.SURI, c.SS58Prefix)
	if err != nil {
		return fmt.Errorf("signer: %w", err)
	}
	c.api, c.layout, c.kp = api, layout, kp
	return nil
}

func (c *CORD) Anchor(ctx context.Context, checkpoint []byte) (Ref, error) {
	if err := c.connect(); err != nil {
		return Ref{}, err
	}
	api := c.api
	meta, err := api.RPC.State.GetMetadataLatest()
	if err != nil {
		return Ref{}, err
	}
	call, err := types.NewCall(meta, "System.remark", checkpoint)
	if err != nil {
		return Ref{}, err
	}
	callEnc, err := codec.Encode(call)
	if err != nil {
		return Ref{}, err
	}
	genesisHash, err := api.RPC.Chain.GetBlockHash(0)
	if err != nil {
		return Ref{}, err
	}
	rv, err := api.RPC.State.GetRuntimeVersionLatest()
	if err != nil {
		return Ref{}, err
	}
	key, err := types.CreateStorageKey(meta, "System", "Account", c.kp.PublicKey)
	if err != nil {
		return Ref{}, err
	}
	var acct types.AccountInfo
	if _, err := api.RPC.State.GetStorageLatest(key, &acct); err != nil {
		return Ref{}, err
	}

	extra, err := encodeExtra(c.layout, uint64(acct.Nonce))
	if err != nil {
		return Ref{}, err
	}
	additional, err := encodeAdditional(c.layout, uint32(rv.SpecVersion), uint32(rv.TransactionVersion), genesisHash[:])
	if err != nil {
		return Ref{}, err
	}
	payload := append(append(append([]byte{}, callEnc...), extra...), additional...)
	if len(payload) > 256 {
		h := blake2b.Sum256(payload)
		payload = h[:]
	}
	sig, err := signature.Sign(payload, c.kp.URI)
	if err != nil {
		return Ref{}, err
	}
	body := []byte{0x84}                        // extrinsic v4, signed
	body = append(body, 0x00)                   // MultiAddress::Id
	body = append(body, c.kp.PublicKey...)      //
	body = append(body, 0x01)                   // MultiSignature::Sr25519
	body = append(body, sig...)                 //
	body = append(body, extra...)               //
	body = append(body, callEnc...)             //
	lenEnc, err := codec.Encode(types.NewUCompactFromUInt(uint64(len(body))))
	if err != nil {
		return Ref{}, err
	}
	ext := append(lenEnc, body...)

	var txHash string
	if err := api.Client.Call(&txHash, "author_submitExtrinsic", fmt.Sprintf("%#x", ext)); err != nil {
		return Ref{}, fmt.Errorf("submit: %w", err)
	}

	// Verify inclusion: scan new blocks for the checkpoint bytes.
	wantHex := hex.EncodeToString(checkpoint)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Ref{TxRef: txHash}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		head, err := api.RPC.Chain.GetHeaderLatest()
		if err != nil {
			continue
		}
		for n := uint64(head.Number); n > 0 && n+3 > uint64(head.Number); n-- {
			bh, err := api.RPC.Chain.GetBlockHash(n)
			if err != nil {
				continue
			}
			block, err := api.RPC.Chain.GetBlock(bh)
			if err != nil {
				continue
			}
			for _, e := range block.Block.Extrinsics {
				enc, err := codec.EncodeToHex(e)
				if err != nil {
					continue
				}
				if strings.Contains(strings.TrimPrefix(enc, "0x"), wantHex) {
					return Ref{TxRef: txHash, BlockRef: bh.Hex()}, nil
				}
			}
		}
	}
	return Ref{TxRef: txHash}, fmt.Errorf("accepted by pool but inclusion not observed within 45s")
}
