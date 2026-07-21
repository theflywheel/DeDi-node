// cord-anchor-poc proves the write path for anchoring dedid checkpoints into a
// CORD chain (spike for the optional ledger-anchor layer).
//
// It fetches the live signed checkpoint from a dedid node, submits it to a CORD
// dev chain as a system.remark extrinsic signed by //Alice, then reads the block
// back and verifies the checkpoint bytes are on chain.
//
// CORD 0.9.9 (Weave) declares a SignedExtra that gsrpc's classic signer does not
// support (ChargeAssetTxPayment, CheckMetadataHash, WeightReclaim), so the
// extrinsic is encoded by hand to match the metadata's extension list exactly:
//
//	extra      = era ++ compact(nonce) ++ compact(tip) ++ asset_id:None ++ mode:Disabled
//	additional = spec_version ++ tx_version ++ genesis ++ era_checkpoint ++ metadata_hash:None
//
//	CORD_RPC_URL   ws endpoint of the CORD node (default ws://127.0.0.1:9945)
//	DEDI_URL       dedid base URL (default https://dedi.proto.theflywheel.in)
package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	gsrpc "github.com/centrifuge/go-substrate-rpc-client/v4"
	"github.com/centrifuge/go-substrate-rpc-client/v4/signature"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types/codec"
	"golang.org/x/crypto/blake2b"
)

const cordSS58Prefix = 29

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func mustEncode(v any) []byte {
	b, err := codec.Encode(v)
	if err != nil {
		log.Fatalf("scale encode %T: %v", v, err)
	}
	return b
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func main() {
	dediURL := envOr("DEDI_URL", "https://dedi.proto.theflywheel.in")
	cordURL := envOr("CORD_RPC_URL", "ws://127.0.0.1:9945")

	// 1. Fetch the live signed checkpoint from dedid.
	resp, err := http.Get(dediURL + "/dedi/log/checkpoint")
	if err != nil {
		log.Fatalf("fetch checkpoint: %v", err)
	}
	checkpoint, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		log.Fatalf("fetch checkpoint: status %d err %v", resp.StatusCode, err)
	}
	lines := strings.Split(strings.TrimSpace(string(checkpoint)), "\n")
	fmt.Printf("[1] dedid checkpoint: origin=%s size=%s root=%.20s... (%d bytes, signed note)\n",
		lines[0], lines[1], lines[2], len(checkpoint))

	// 2. Connect to CORD.
	api, err := gsrpc.NewSubstrateAPI(cordURL)
	if err != nil {
		log.Fatalf("connect CORD: %v", err)
	}
	chain, _ := api.RPC.System.Chain()
	meta, err := api.RPC.State.GetMetadataLatest()
	if err != nil {
		log.Fatalf("metadata: %v", err)
	}
	genesisHash, err := api.RPC.Chain.GetBlockHash(0)
	if err != nil {
		log.Fatalf("genesis hash: %v", err)
	}
	rv, err := api.RPC.State.GetRuntimeVersionLatest()
	if err != nil {
		log.Fatalf("runtime version: %v", err)
	}
	fmt.Printf("[2] connected to CORD chain %q (spec %d)\n", chain, rv.SpecVersion)

	// 3. Build system.remark(checkpoint) and sign with a hand-encoded extra
	// matching CORD's SignedExtra (see doc comment).
	call, err := types.NewCall(meta, "System.remark", checkpoint)
	if err != nil {
		log.Fatalf("build call: %v", err)
	}
	callEnc := mustEncode(call)

	alice, err := signature.KeyringPairFromSecret("//Alice", cordSS58Prefix)
	if err != nil {
		log.Fatalf("keypair: %v", err)
	}
	key, err := types.CreateStorageKey(meta, "System", "Account", alice.PublicKey)
	if err != nil {
		log.Fatalf("storage key: %v", err)
	}
	var acct types.AccountInfo
	if ok, err := api.RPC.State.GetStorageLatest(key, &acct); err != nil || !ok {
		log.Fatalf("account info: ok=%v err=%v (is //Alice endowed on this chain?)", ok, err)
	}

	extra := cat(
		[]byte{0x00}, // CheckMortality: immortal era
		mustEncode(types.NewUCompactFromUInt(uint64(acct.Nonce))), // CheckNonce
		mustEncode(types.NewUCompactFromUInt(0)),                  // ChargeAssetTxPayment.tip
		[]byte{0x00}, // ChargeAssetTxPayment.asset_id = None
		[]byte{0x00}, // CheckMetadataHash mode = Disabled
	)
	additional := cat(
		mustEncode(rv.SpecVersion),        // CheckSpecVersion
		mustEncode(rv.TransactionVersion), // CheckTxVersion
		genesisHash[:],                    // CheckGenesis
		genesisHash[:],                    // CheckMortality (immortal -> genesis)
		[]byte{0x00},                      // CheckMetadataHash additional = None
	)
	payload := cat(callEnc, extra, additional)
	if len(payload) > 256 {
		h := blake2b.Sum256(payload)
		payload = h[:]
	}
	sig, err := signature.Sign(payload, alice.URI)
	if err != nil {
		log.Fatalf("sign: %v", err)
	}
	body := cat(
		[]byte{0x84},              // extrinsic v4, signed
		[]byte{0x00}, alice.PublicKey, // MultiAddress::Id
		[]byte{0x01}, sig, // MultiSignature::Sr25519
		extra, callEnc,
	)
	ext := cat(mustEncode(types.NewUCompactFromUInt(uint64(len(body)))), body)
	fmt.Printf("[3] hand-encoded system.remark as //Alice (nonce %d, %d bytes)\n",
		acct.Nonce, len(ext))

	// 4. Submit (fire-and-poll: author_submitExtrinsic + scan new blocks).
	var txHash string
	if err := api.Client.Call(&txHash, "author_submitExtrinsic", fmt.Sprintf("%#x", ext)); err != nil {
		log.Fatalf("submit: %v", err)
	}
	fmt.Printf("[4] accepted by pool, tx hash %s\n", txHash)

	// 5. Poll for inclusion, then verify the checkpoint bytes on chain.
	wantHex := hex.EncodeToString(checkpoint)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		head, err := api.RPC.Chain.GetHeaderLatest()
		if err != nil {
			continue
		}
		// scan the last few blocks
		for n := uint64(head.Number); n > 0 && n+3 > uint64(head.Number); n-- {
			bh, err := api.RPC.Chain.GetBlockHash(n)
			if err != nil {
				continue
			}
			block, err := api.RPC.Chain.GetBlock(bh)
			if err != nil {
				continue
			}
			for i, e := range block.Block.Extrinsics {
				enc, err := codec.EncodeToHex(e)
				if err != nil {
					continue
				}
				if strings.Contains(strings.TrimPrefix(enc, "0x"), wantHex) {
					fmt.Printf("[5] VERIFIED: checkpoint bytes found in block #%d extrinsic %d (%s)\n",
						n, i, bh.Hex())
					fmt.Printf("\nPOC PASS: dedid checkpoint (size %s) anchored to CORD %q at %s\n",
						lines[1], chain, bh.Hex())
					return
				}
			}
		}
	}
	log.Fatal("timed out: extrinsic accepted but not found in a block within 60s")
}
