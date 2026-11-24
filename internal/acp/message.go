package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/session"
)

// The standard ACP methods acp gives a Kind itself, whoever the client is.
const (
	methodInitialize = "initialize"
	methodList       = "session/list"
	methodCancel     = "$/cancel_request"
)

// standard are the client's standard requests that mean more to a session
// than to be carried.
var standard = map[string]session.Kind{
	methodInitialize: session.Connect,
	methodList:       session.List,
	"session/new":    session.Open,
	"session/load":   session.Open,
	"session/resume": session.Open,
}

// namesDir are the methods whose cwd is the directory a session routes by.
var namesDir = map[string]bool{
	"session/new":    true,
	"session/load":   true,
	"session/resume": true,
	methodList:       true,
}

// errInternal is JSON-RPC's code for an error of the answering side.
const errInternal = -32603

// envelope is one JSON-RPC message as ACP writes it. Its parts stay raw, so a
// message carries what the other side wrote even where acp reads none of it.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// message is one ACP message with the Kind its connection gave it. A reply to
// a notification is silent: JSON-RPC never answers one, so Send writes
// nothing for it.
type message struct {
	env    envelope
	kind   session.Kind
	silent bool
}

var _ session.Message = message{}

// parse reads one line into an envelope. A line that is no JSON-RPC message
// is an error: what follows it on the stream cannot be trusted to be one.
func parse(line []byte) (envelope, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return envelope{}, fmt.Errorf("acp: not a message: %w", err)
	}
	// An answer names its request, or null when it answers one that could
	// not be read.
	if env.Method == "" && (env.ID == nil || (env.Result == nil && env.Error == nil)) {
		return envelope{}, fmt.Errorf("acp: neither a request, a notification nor an answer: %.200s", line)
	}
	return env, nil
}

// present reports whether a raw member was written with a value: an absent id
// and an id of null both name no request.
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (m message) Kind() session.Kind { return m.kind }

// ID is the request the message is or answers, and for a cancel the request
// it cancels, which ACP writes in its params.
func (m message) ID() session.MessageID {
	if m.env.Method == methodCancel {
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.env.Params, &p) != nil {
			return ""
		}
		return idOf(p.RequestID)
	}
	return idOf(m.env.ID)
}

// Chat is the sessionId the message names: in its params when it is a request
// or a notification, in its result when it answers one, as the answer to a
// session/new names the chat it made.
func (m message) Chat() session.ChatID {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	body := m.env.Params
	if m.env.Method == "" {
		body = m.env.Result
	}
	if json.Unmarshal(body, &p) != nil {
		return ""
	}
	return session.ChatID(p.SessionID)
}

func (m message) Dir() session.Path {
	if !namesDir[m.env.Method] {
		return ""
	}
	var p struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(m.env.Params, &p) != nil {
		return ""
	}
	return session.Path(p.Cwd)
}

func (m message) WithID(id session.MessageID) session.Message {
	if m.env.Method == methodCancel {
		params, err := setMember(m.env.Params, "requestId", rawID(id))
		if err != nil {
			return m
		}
		m.env.Params = params
		return m
	}
	m.env.ID = rawID(id)
	return m
}

// Mapped maps every string of the params, the result and the error that is a
// path under a root, or a file URI of one: ACP carries paths in many fields of
// many methods, and names a file in a prompt's resource or resource link by its
// URI, and either must name the same file on the other workstation.
func (m message) Mapped(path func(session.Path) session.Path) session.Message {
	mapString := func(s string) string {
		if strings.HasPrefix(s, fileScheme+"://") {
			return mapFileURI(s, path)
		}
		return string(path(session.Path(s)))
	}
	for _, part := range []*json.RawMessage{&m.env.Params, &m.env.Result, &m.env.Error} {
		if *part == nil {
			continue
		}
		// NOTE: each part was decoded from a valid message or encoded by acp,
		// so it is valid JSON and pathmap.JSON cannot fail on it.
		if mapped, err := pathmap.JSON(*part, mapString); err == nil {
			*part = mapped
		}
	}
	return m
}

const fileScheme = "file"

// mapFileURI maps the path of a file URI on this workstation; a URI that does
// not parse, or names another host, is left as written.
func mapFileURI(s string, path func(session.Path) session.Path) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != fileScheme || (u.Host != "" && u.Host != "localhost") {
		return s
	}
	mapped := string(path(session.Path(u.Path)))
	if mapped == u.Path {
		return s
	}
	u.Path, u.RawPath = mapped, ""
	return u.String()
}

func (m message) Reply(result session.Result) session.Message {
	var body any
	switch r := result.(type) {
	case session.EmptyResult:
		body = struct{}{}
	case session.HandshakeResult:
		body = initializeResult{
			ProtocolVersion:   r.Handshake.Version(),
			AgentCapabilities: r.Handshake.Capabilities(),
			AuthMethods:       []json.RawMessage{},
		}
	case session.ListResult:
		body = listResult(r.Conversations)
	default:
		return m.Fail(fmt.Errorf("hermod: no answer of type %T", result))
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return m.Fail(fmt.Errorf("hermod: write the answer: %w", err))
	}
	return m.answer(envelope{Result: raw})
}

func (m message) Fail(err error) session.Message {
	// NOTE: a struct of an int and a string always encodes.
	raw, _ := json.Marshal(rpcError{Code: errInternal, Message: err.Error()})
	return m.answer(envelope{Error: raw})
}

// answer is the response to this request carrying the result or error of
// body; a notification gets a silent one.
func (m message) answer(body envelope) message {
	if m.env.Method == "" || !present(m.env.ID) {
		return message{silent: true}
	}
	body.JSONRPC = "2.0"
	body.ID = m.env.ID
	return message{env: body}
}

// WithoutClientCapabilities drops the client's fs and terminal capabilities
// from an initialize, so an adapter reads, writes and runs on the remote
// rather than asking the client to on its own workstation.
func (m message) WithoutClientCapabilities() session.Message {
	if m.env.Method != methodInitialize {
		return m
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(m.env.Params, &params) != nil || params == nil {
		return m
	}
	var caps map[string]json.RawMessage
	if json.Unmarshal(params["clientCapabilities"], &caps) != nil || caps == nil {
		return m
	}
	delete(caps, "fs")
	delete(caps, "terminal")
	rawCaps, err := json.Marshal(caps)
	if err != nil {
		return m
	}
	params["clientCapabilities"] = rawCaps
	rawParams, err := json.Marshal(params)
	if err != nil {
		return m
	}
	m.env.Params = rawParams
	return m
}

// Handshake reads an answer to initialize. ACP lets an agent leave out its
// capabilities, which then offer nothing; a version it must give.
func (m message) Handshake() (agent.Handshake, bool) {
	if m.env.Method != "" || m.env.Result == nil {
		return agent.Handshake{}, false
	}
	var r struct {
		ProtocolVersion   *int            `json:"protocolVersion"`
		AgentCapabilities json.RawMessage `json:"agentCapabilities"`
	}
	if json.Unmarshal(m.env.Result, &r) != nil || r.ProtocolVersion == nil {
		return agent.Handshake{}, false
	}
	caps := r.AgentCapabilities
	if !present(caps) {
		caps = json.RawMessage(`{}`)
	}
	h, err := agent.NewHandshake(*r.ProtocolVersion, caps)
	return h, err == nil
}

// initializeResult is ACP's answer to initialize. Hermod asks for no
// authentication of its own: an adapter that needs one is started on the
// remote, where its credentials are.
type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities json.RawMessage   `json:"agentCapabilities"`
	AuthMethods       []json.RawMessage `json:"authMethods"`
}

type sessionInfo struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// listResult is ACP's answer to session/list. A conversation's state is the
// agent program's own and has no place in it.
func listResult(conversations []agent.Conversation) sessionList {
	sessions := make([]sessionInfo, 0, len(conversations))
	for _, c := range conversations {
		info := sessionInfo{SessionID: c.ID, Cwd: c.Dir, Title: c.Title}
		if !c.Updated.IsZero() {
			info.UpdatedAt = c.Updated.UTC().Format(time.RFC3339)
		}
		sessions = append(sessions, info)
	}
	return sessionList{Sessions: sessions}
}

type sessionList struct {
	Sessions []sessionInfo `json:"sessions"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// setMember is the JSON object raw with member key set to value.
func setMember(raw json.RawMessage, key string, value json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("acp: not a JSON object")
	}
	object[key] = value
	return json.Marshal(object)
}
