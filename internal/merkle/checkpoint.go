package merkle

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"
)

// FormatCheckpoint renders the C2SP checkpoint body:
//
//	<origin>\n<size>\n<base64 root>\n
func FormatCheckpoint(origin string, size int64, root tlog.Hash) string {
	return fmt.Sprintf("%s\n%d\n%s\n", origin, size, base64.StdEncoding.EncodeToString(root[:]))
}

func ParseCheckpoint(text string) (origin string, size int64, root tlog.Hash, err error) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 3 {
		return "", 0, root, fmt.Errorf("checkpoint: want >=3 lines, got %d", len(lines))
	}
	origin = lines[0]
	size, err = strconv.ParseInt(lines[1], 10, 64)
	if err != nil {
		return "", 0, root, fmt.Errorf("checkpoint size: %w", err)
	}
	rb, err := base64.StdEncoding.DecodeString(lines[2])
	if err != nil || len(rb) != len(root) {
		return "", 0, root, fmt.Errorf("checkpoint root hash invalid")
	}
	copy(root[:], rb)
	return origin, size, root, nil
}

// SignCheckpoint signs the checkpoint body as a sumdb note with the node
// identity key (skey as produced by note.GenerateKey).
func SignCheckpoint(skey, origin string, size int64, root tlog.Hash) (string, error) {
	signer, err := note.NewSigner(skey)
	if err != nil {
		return "", err
	}
	msg, err := note.Sign(&note.Note{Text: FormatCheckpoint(origin, size, root)}, signer)
	if err != nil {
		return "", err
	}
	return string(msg), nil
}
