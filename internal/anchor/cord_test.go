package anchor

import (
	"bytes"
	"testing"
)

// The Weave extension list observed on both cord --dev (spec 9900) and
// weave1.testnet.cord.network (spec 9800); proven working over the wire by the
// cord-anchor POC.
var weaveExts = []string{
	"CheckNonZeroSender", "CheckSpecVersion", "CheckTxVersion", "CheckGenesis",
	"CheckMortality", "CheckNonce", "CheckWeight", "ChargeAssetTxPayment",
	"CheckMetadataHash", "WeightReclaim",
}

// The Loom (governed runtime) list per runtimes/loom/src/lib.rs: plain
// ChargeTransactionPayment instead of ChargeAssetTxPayment.
var loomExts = []string{
	"CheckNonZeroSender", "CheckSpecVersion", "CheckTxVersion", "CheckGenesis",
	"CheckMortality", "CheckNonce", "CheckWeight", "ChargeTransactionPayment",
	"CheckMetadataHash", "WeightReclaim",
}

func TestDetectLayoutWeave(t *testing.T) {
	l, err := detectLayout(weaveExts)
	if err != nil {
		t.Fatal(err)
	}
	if !l.assetTxPayment || !l.metadataHash {
		t.Fatalf("weave layout wrong: %+v", l)
	}
}

func TestDetectLayoutLoom(t *testing.T) {
	l, err := detectLayout(loomExts)
	if err != nil {
		t.Fatal(err)
	}
	if l.assetTxPayment || !l.metadataHash {
		t.Fatalf("loom layout wrong: %+v", l)
	}
}

func TestDetectLayoutRejectsUnknown(t *testing.T) {
	_, err := detectLayout([]string{"CheckNonce", "CheckNetworkMembership"})
	if err == nil {
		t.Fatal("expected unknown extension to be rejected")
	}
}

// Golden bytes: the exact extra encoding the POC proved against a live chain
// (immortal era, nonce 0, tip 0, asset None, metadata-hash mode Disabled).
func TestEncodeExtraWeaveGolden(t *testing.T) {
	l := extraLayout{assetTxPayment: true, metadataHash: true}
	got, err := encodeExtra(l, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x00, 0x00, 0x00, 0x00} // era, nonce, tip, asset:None, mode:Disabled
	if !bytes.Equal(got, want) {
		t.Fatalf("extra = %x, want %x", got, want)
	}
}

func TestEncodeExtraLoom(t *testing.T) {
	l := extraLayout{assetTxPayment: false, metadataHash: true}
	got, err := encodeExtra(l, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x14, 0x00, 0x00} // era, compact(5)=0x14, tip, mode
	if !bytes.Equal(got, want) {
		t.Fatalf("extra = %x, want %x", got, want)
	}
}

func TestEncodeAdditionalShape(t *testing.T) {
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	l := extraLayout{metadataHash: true}
	got, err := encodeAdditional(l, 9900, 2, genesis)
	if err != nil {
		t.Fatal(err)
	}
	// u32 spec + u32 tx + 32B genesis + 32B era checkpoint + 1B Option:None
	if len(got) != 4+4+32+32+1 {
		t.Fatalf("additional length = %d, want 73", len(got))
	}
	if got[len(got)-1] != 0x00 {
		t.Fatal("metadata-hash Option should end None")
	}
	noHash, err := encodeAdditional(extraLayout{}, 9900, 2, genesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(noHash) != 72 {
		t.Fatalf("additional without metadata hash = %d, want 72", len(noHash))
	}
}
