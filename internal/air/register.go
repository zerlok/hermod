package air

import (
	"errors"

	"github.com/zerlok/hermod/internal/acp"
	"github.com/zerlok/hermod/internal/session"
)

// Register adds Air's own _session/* methods to r. Each names a chat, and is
// answered with nothing but its _meta.
func Register(r *acp.Registry) {
	acp.Request[ArchiveParams, ArchiveResult](r, "_session/archive", acp.Client)
	acp.Request[UnarchiveParams, UnarchiveResult](r, "_session/unarchive", acp.Client)
	acp.Request[RenameParams, RenameResult](r, "_session/rename", acp.Client)
}

// ArchiveParams mark a chat done, the one message of Air's that ends a chat.
type ArchiveParams struct{ acp.ChatParams }

func (ArchiveParams) Kind() session.Kind { return session.Close }

// ArchiveResult answers a _session/archive.
type ArchiveResult struct{ acp.EmptyResult }

// UnarchiveParams are sent before Air opens a chat marked done again.
type UnarchiveParams struct{ acp.ChatParams }

// UnarchiveResult answers a _session/unarchive.
type UnarchiveResult struct{ acp.EmptyResult }

// RenameParams give a chat a title.
type RenameParams struct {
	acp.ChatParams
	Title string `json:"title"`
}

func (p RenameParams) Validate() error {
	if err := p.ChatParams.Validate(); err != nil {
		return err
	}
	if p.Title == "" {
		return errors.New("title is required")
	}
	return nil
}

// RenameResult answers a _session/rename.
type RenameResult struct{ acp.EmptyResult }
