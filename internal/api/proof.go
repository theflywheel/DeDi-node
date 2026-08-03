package api

import (
	"encoding/base64"
	"encoding/hex"
	"errors"

	"net/http"

	"github.com/theflywheel/DeDi-node/internal/store"
)

type leafDTO struct {
	EntryType  string `json:"entry_type"`
	Namespace  string `json:"namespace"`
	Registry   string `json:"registry"`
	RecordName string `json:"record_name"`
	VersionNum int32  `json:"version_num"`
	Digest     string `json:"digest"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
}

type proofDTO struct {
	LeafIndex  int64    `json:"leaf_index"`
	TreeSize   int64    `json:"tree_size"`
	Checkpoint string   `json:"checkpoint"`
	Path       []string `json:"path"`
	Leaf       leafDTO  `json:"leaf"`
}

// buildProof returns an inclusion proof for e against a checkpoint that
// covers it, publishing a fresh checkpoint if needed.
func (s *Server) buildProof(r *http.Request, e store.Entry) (*proofDTO, error) {
	ctx := r.Context()
	size, cpNote, err := s.Store.LatestCheckpoint(ctx)
	if errors.Is(err, store.ErrNoCheckpoint) || (err == nil && size <= e.Seq) {
		// A node assembled without a checkpointer cannot mint one on demand;
		// answer as an error rather than panicking inside the handler.
		if s.CP == nil {
			return nil, errors.New("no checkpoint covers this entry and this node has no checkpointer")
		}
		size, cpNote, err = s.CP.PublishNow(ctx)
	}
	if err != nil {
		return nil, err
	}
	proof, err := s.Store.ProveInclusion(ctx, size, e.Seq)
	if err != nil {
		return nil, err
	}
	path := make([]string, len(proof))
	for i, h := range proof {
		path[i] = base64.StdEncoding.EncodeToString(h[:])
	}
	return &proofDTO{
		LeafIndex: e.Seq, TreeSize: size, Checkpoint: cpNote, Path: path,
		Leaf: leafDTO{
			EntryType: e.EntryType, Namespace: e.Namespace, Registry: e.Registry,
			RecordName: e.RecordName, VersionNum: e.VersionNum,
			Digest: hex.EncodeToString(e.Digest), CreatedBy: e.CreatedBy,
			CreatedAt: fmtTime(e.CreatedAt),
		},
	}, nil
}
