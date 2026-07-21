// dump prints CORD's declared signed extensions (metadata v14) so the POC can
// construct a matching extrinsic extra.
package main

import (
	"fmt"
	"log"
	"os"

	gsrpc "github.com/centrifuge/go-substrate-rpc-client/v4"
)

func main() {
	url := os.Getenv("CORD_RPC_URL")
	if url == "" {
		url = "ws://127.0.0.1:9945"
	}
	api, err := gsrpc.NewSubstrateAPI(url)
	if err != nil {
		log.Fatal(err)
	}
	meta, err := api.RPC.State.GetMetadataLatest()
	if err != nil {
		log.Fatal(err)
	}
	m := meta.AsMetadataV14
	fmt.Println("extrinsic version:", m.Extrinsic.Version)
	for i, se := range m.Extrinsic.SignedExtensions {
		t, _ := m.EfficientLookup[se.Type.Int64()]
		add, _ := m.EfficientLookup[se.AdditionalSigned.Int64()]
		tn, an := "?", "?"
		if t != nil {
			tn = fmt.Sprint(t.Path)
		}
		if add != nil {
			an = fmt.Sprint(add.Path, " def:", add.Def)
		}
		fmt.Printf("%2d %-28s extra=%v additional=%v\n", i, se.Identifier, tn, an)
	}
}
