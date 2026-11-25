package air

import (
	"fmt"

	"github.com/zerlok/hermod/internal/acp"
	"github.com/zerlok/hermod/internal/session"
)

// Serializer reads and writes the messages of Air's ACP connection: ACP's
// standard ones, and Air's own.
type Serializer struct{ acp.Standard }

// NewSerializer is the serializer of Air's connection.
func NewSerializer() Serializer {
	return Serializer{acp.NewStandard().With(
		acp.RequestOf[CloseParams, CloseResult](acp.Client),
		acp.RequestOf[ArchiveParams, ArchiveResult](acp.Client),
		acp.RequestOf[UnarchiveParams, UnarchiveResult](acp.Client),
		acp.RequestOf[RenameParams, RenameResult](acp.Client),
	)}
}

// CloseParams is ACP's session/close as Air sends it: for every tab it closes
// and at an IDE quit, so it only hides the chat. An adapter that received it
// would end the chat.
type CloseParams struct {
	SessionID acp.SessionID `json:"sessionId"`
	Meta      acp.Meta      `json:"_meta,omitzero"`
}

func (CloseParams) Method() string         { return "session/close" }
func (CloseParams) Kind() session.Kind     { return session.Suspend }
func (p CloseParams) Chat() session.ChatID { return session.ChatID(p.SessionID) }
func (CloseParams) Dir() session.Path      { return "" }

// CloseResult answers a session/close, which session answers itself.
type CloseResult struct {
	Meta acp.Meta `json:"_meta,omitzero"`
}

func (CloseResult) Chat() session.ChatID { return "" }

// FromSession is the answer to a session/close that has nothing to return.
func (CloseResult) FromSession(r session.Result) (CloseResult, error) {
	if _, ok := r.(session.EmptyResult); !ok {
		return CloseResult{}, fmt.Errorf("hermod: session/close is not answered with a %T", r)
	}
	return CloseResult{}, nil
}

// ArchiveParams is Air's "mark done", the one message that ends a chat.
type ArchiveParams struct {
	SessionID acp.SessionID `json:"sessionId"`
	Meta      acp.Meta      `json:"_meta,omitzero"`
}

func (ArchiveParams) Method() string         { return "_session/archive" }
func (ArchiveParams) Kind() session.Kind     { return session.Close }
func (p ArchiveParams) Chat() session.ChatID { return session.ChatID(p.SessionID) }
func (ArchiveParams) Dir() session.Path      { return "" }

// ArchiveResult answers an _session/archive.
type ArchiveResult struct {
	Meta acp.Meta `json:"_meta,omitzero"`
}

func (ArchiveResult) Chat() session.ChatID { return "" }

// UnarchiveParams undoes a "mark done", before Air opens the chat again.
type UnarchiveParams struct {
	SessionID acp.SessionID `json:"sessionId"`
	Meta      acp.Meta      `json:"_meta,omitzero"`
}

func (UnarchiveParams) Method() string         { return "_session/unarchive" }
func (UnarchiveParams) Kind() session.Kind     { return session.Pass }
func (p UnarchiveParams) Chat() session.ChatID { return session.ChatID(p.SessionID) }
func (UnarchiveParams) Dir() session.Path      { return "" }

// UnarchiveResult answers an _session/unarchive.
type UnarchiveResult struct {
	Meta acp.Meta `json:"_meta,omitzero"`
}

func (UnarchiveResult) Chat() session.ChatID { return "" }

// RenameParams gives a chat a title.
type RenameParams struct {
	SessionID acp.SessionID `json:"sessionId"`
	Title     string        `json:"title"`
	Meta      acp.Meta      `json:"_meta,omitzero"`
}

func (RenameParams) Method() string         { return "_session/rename" }
func (RenameParams) Kind() session.Kind     { return session.Pass }
func (p RenameParams) Chat() session.ChatID { return session.ChatID(p.SessionID) }
func (RenameParams) Dir() session.Path      { return "" }

// RenameResult answers an _session/rename.
type RenameResult struct {
	Meta acp.Meta `json:"_meta,omitzero"`
}

func (RenameResult) Chat() session.ChatID { return "" }
