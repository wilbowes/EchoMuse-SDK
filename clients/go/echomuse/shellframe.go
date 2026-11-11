package echomuse

import "strings"

// CommandSentinel is the marker the controller waits for.
//
// The controller appends " ; echo 'SENTINEL'" to every command it writes
// on the shell plane and reads the output until it sees the marker. A
// device that answers without it leaves the controller waiting for the
// whole of its timeout — thirty seconds for a transfer, five for a
// command — and a controller that waits is a controller that does nothing
// else.
//
// Every answer a plane gives ends with it.
const CommandSentinel = "__CMD_DONE_9f3a__"

// FrameAnswer puts the marker on an answer.
//
// The answer is the plane's own text with the marker after it. A plane
// that has nothing to say says just the marker: the controller asked a
// question and the device answered it, and an answer with nothing in it
// is an answer.
func FrameAnswer(text string) string {
	// The marker is not repeated. A plane that has already framed its
	// answer is not framed twice.
	if strings.Contains(text, CommandSentinel) {
		return text
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + CommandSentinel + "\n"
}
