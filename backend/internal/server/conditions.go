package server

import (
	"fmt"
	"slices"
	"time"

	"continuum/internal/agentcaps"
)

// A Condition is one fact about an agent that is either true, false or not known, with a short code for why and a sentence for a
// person. It is computed, from what the hub holds and the server's clock, by agentConditions and by nothing else; the document
// carries it and no screen reads it yet. A condition is a statement about now and not a record of events: the audit trail is the
// record.
//
// Status is "True", "False" or "Unknown", and Unknown is a real answer: "this server cannot tell", which is not "no".
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// Since is when the condition became what it is, as far as the server knows ("" when it does not).
	Since string `json:"since,omitempty"`
}

const (
	CondTrue    = "True"
	CondFalse   = "False"
	CondUnknown = "Unknown"

	CondConnected     = "Connected"
	CondConfigInForce = "ConfigInForce"
	CondCompatible    = "Compatible"
)

// condInputs is everything agentConditions looks at, copied out of the view so the function itself touches no lock and no clock.
type condInputs struct {
	// Seen is whether the agent has connected at least once since this server started.
	Seen           bool
	Connected      bool
	ConnectedSince time.Time
	Protocol       uint32
	Caps           []string
	// CfgSent is the digest of the Config last put on the stream, CfgApplied the digest the agent last named ("" if it has not).
	CfgSent, CfgApplied string
	CfgSentAt           time.Time
	CfgInForceAt        time.Time
}

func since(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return rfc(t)
}

// agentConditions is the one place the conditions are decided. It never blocks anything: a condition that is False is a thing to
// tell a person, and no part of the protocol reads it.
func agentConditions(in condInputs, now time.Time) []Condition {
	return []Condition{connectedCondition(in), configInForceCondition(in, now), compatibleCondition(in)}
}

func connectedCondition(in condInputs) Condition {
	if in.Connected {
		return Condition{Type: CondConnected, Status: CondTrue, Reason: "Open", Message: "a stream from the agent is open", Since: since(in.ConnectedSince)}
	}
	msg := "no stream from the agent is open"
	if !in.Seen {
		msg += " (it has not connected since this server started)"
	}
	return Condition{Type: CondConnected, Status: CondFalse, Reason: "NoStream", Message: msg}
}

// configInForceCondition says whether the Config the server last sent is the one the agent says it has in force. It follows an echo
// that was actually seen: an agent that never states a protocol version but does name its Config is judged by what it named.
func configInForceCondition(in condInputs, now time.Time) Condition {
	c := Condition{Type: CondConfigInForce}
	switch {
	case !in.Connected:
		c.Status, c.Reason, c.Message = CondUnknown, "NotConnected", "the agent is not connected, so what it has in force is not known"
	case in.CfgApplied != "" && in.CfgApplied == in.CfgSent:
		c.Status, c.Reason, c.Message, c.Since = CondTrue, "Echoed", "the agent names the Config last sent as the one in force", since(in.CfgInForceAt)
	case in.CfgApplied != "":
		c.Status, c.Reason, c.Since = CondFalse, "NotEchoed", since(in.CfgSentAt)
		c.Message = fmt.Sprintf("the agent names an earlier Config as the one in force; the latest was sent %s ago", age(now, in.CfgSentAt))
	case slices.Contains(in.Caps, agentcaps.ConfigDigest):
		// It says it echoes, and it has not.
		c.Status, c.Reason, c.Since = CondFalse, "NotEchoed", since(in.CfgSentAt)
		c.Message = fmt.Sprintf("the agent says it names the Config it has in force and has not named the one sent %s ago", age(now, in.CfgSentAt))
	default:
		c.Status, c.Reason, c.Message = CondUnknown, "AgentDoesNotEcho", "this agent does not say which Config it has in force"
	}
	return c
}

func compatibleCondition(in condInputs) Condition {
	c := Condition{Type: CondCompatible}
	switch {
	case !in.Seen:
		c.Status, c.Reason, c.Message = CondUnknown, "NotSeenSinceStart", "the agent has not connected since this server started, so the protocol it speaks is not known"
	case in.Protocol > agentcaps.ProtocolMax:
		c.Status, c.Reason = CondFalse, "ProtocolTooNew"
		c.Message = fmt.Sprintf("the agent speaks protocol %d and this server knows up to %d; the stream is served with what the server understands", in.Protocol, agentcaps.ProtocolMax)
	case in.Protocol < agentcaps.ProtocolMin:
		c.Status, c.Reason = CondFalse, "ProtocolTooOld"
		c.Message = fmt.Sprintf("the agent speaks protocol %d and this server serves %d and newer", in.Protocol, agentcaps.ProtocolMin)
	case in.Protocol == 0:
		c.Status, c.Reason, c.Message = CondTrue, "LegacyAgent", "the agent is from before protocol versions and is served as it always was"
	default:
		c.Status, c.Reason, c.Message = CondTrue, "Current", fmt.Sprintf("the agent speaks protocol %d", in.Protocol)
	}
	return c
}

// age is a duration for a sentence: whole seconds under a minute, whole minutes under an hour, otherwise hours.
func age(now, then time.Time) string {
	if then.IsZero() || now.Before(then) {
		return "an unknown time"
	}
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// condInputsOf copies what agentConditions needs out of a view (nil: an agent the hub has no view of). The caller holds h.mu.
func condInputsOf(v *view) condInputs {
	if v == nil {
		return condInputs{}
	}
	return condInputs{
		Seen: v.link.connects > 0, Connected: v.connected, ConnectedSince: v.link.since,
		Protocol: v.ext.protocol, Caps: v.ext.caps,
		CfgSent: v.cfgHash, CfgApplied: v.cfgApplied, CfgSentAt: v.cfgSentAt, CfgInForceAt: v.cfgInForceAt,
	}
}
