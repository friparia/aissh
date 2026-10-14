package app

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
)

func newExecutionMarker() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func executionMarkerPrefix(marker string) string {
	return "\x1b]777;aissh-" + marker + ";"
}

func executionTextMarkerPrefix(marker string) string {
	return "__AISSH_DONE_" + marker + "__:"
}

func isolateGatewayApprovedCommand(command, marker string) string {
	// Keep the random token separate from the format string so command echo
	// cannot contain the actual marker. Gateways may filter OSC sequences.
	completion := "printf '\\n__AISSH_DONE_%s__:%s\\n' " + quotePOSIXShell(marker) + " \"$__aissh_status\""
	return isolateApprovedCommandWithCompletion(command, completion, approvedCommandLineEnding())
}

// The inner subshell contains all proposal side effects, including exit, exec,
// traps and stdout redirection. The outer subshell records its status and emits
// a completion marker without installing a trap. Eval keeps syntax errors
// inside the inner shell as well. Only printf emits the actual OSC bytes, so
// terminal echo cannot impersonate command completion.
func isolateApprovedCommand(command, marker string) string {
	return isolateApprovedCommandWithLineEnding(command, marker, approvedCommandLineEnding())
}

func isolateApprovedCommandWithLineEnding(command, marker, lineEnding string) string {
	completion := "printf '\\033]777;aissh-%s;%s\\007' " + quotePOSIXShell(marker) + " \"$__aissh_status\""
	return isolateApprovedCommandWithCompletion(command, completion, lineEnding)
}

func isolateApprovedCommandWithCompletion(command, completion, lineEnding string) string {
	command = strings.ReplaceAll(command, "\r\n", "\n")
	command = strings.ReplaceAll(command, "\r", "\n")
	// Keep the wrapper on one input line. If the command is on its own line,
	// an interactive gateway can enter an approval prompt while the writer is
	// still sending the wrapper's closing lines; those bytes then become menu
	// input. With a single line, the shell starts eval only after the final CR.
	// The inner subshell isolates command side effects; the outer one records
	// its exit status and emits the marker explicitly, without a completion trap.
	wrapper := "( (eval " + quotePOSIXShell(command) + "); __aissh_status=$?; " + completion + " )"
	return strings.ReplaceAll(wrapper, "\n", lineEnding) + "\r"
}

// consume removes only this execution's complete marker. It retains partial
// markers across PTY reads while passing ordinary output through immediately.
// Output after the marker belongs to the next shell prompt, not this command.
func (state *executionState) consume(data []byte) (output, after []byte, exitCode *int) {
	buffer := append(state.Pending, data...)
	state.Pending = nil
	prefix := []byte(executionMarkerPrefix(state.Marker))
	if state.TextMarker {
		prefix = []byte(executionTextMarkerPrefix(state.Marker))
	}
	if state.EchoMarker {
		prefix = []byte("-- FINISHED " + state.Marker)
	}
	for len(buffer) > 0 {
		index := bytes.Index(buffer, prefix)
		if index < 0 {
			keep := len(prefix) - 1
			if keep > len(buffer) {
				keep = len(buffer)
			}
			for keep > 0 && !bytes.Equal(buffer[len(buffer)-keep:], prefix[:keep]) {
				keep--
			}
			output = append(output, buffer[:len(buffer)-keep]...)
			state.Pending = append([]byte(nil), buffer[len(buffer)-keep:]...)
			return output, nil, nil
		}
		output = append(output, buffer[:index]...)
		buffer = buffer[index:]
		status := buffer[len(prefix):]
		if state.EchoMarker {
			if len(status) == 0 || (status[0] == '\r' && len(status) == 1) {
				state.Pending = append([]byte(nil), buffer...)
				return output, nil, nil
			}
			terminator := 0
			if status[0] == '\n' {
				terminator = 1
			} else if status[0] == '\r' && status[1] == '\n' {
				terminator = 2
			}
			if terminator > 0 {
				state.ReceiptReceived = true
				return output, status[terminator:], nil
			}
			output = append(output, buffer[0])
			buffer = buffer[1:]
			continue
		}
		digits := 0
		for digits < len(status) && digits < 3 && status[digits] >= '0' && status[digits] <= '9' {
			digits++
		}
		if digits == len(status) {
			state.Pending = append([]byte(nil), buffer...)
			return output, nil, nil
		}
		terminator := 0
		if state.TextMarker {
			if status[digits] == '\n' {
				terminator = 1
			} else if status[digits] == '\r' {
				if digits+1 == len(status) {
					state.Pending = append([]byte(nil), buffer...)
					return output, nil, nil
				}
				if status[digits+1] == '\n' {
					terminator = 2
				}
			}
		} else if status[digits] == '\a' {
			terminator = 1
		}
		if digits > 0 && terminator > 0 {
			code, err := strconv.Atoi(string(status[:digits]))
			if err == nil && code <= 255 {
				return output, status[digits+terminator:], &code
			}
		}
		// A malformed marker is ordinary output. Advance so it cannot stall
		// collection or grow the pending buffer without bound.
		output = append(output, buffer[0])
		buffer = buffer[1:]
	}
	return output, nil, nil
}
