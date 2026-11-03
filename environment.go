package tclientlib

const (
	environmentIS      byte = 0
	environmentVAR     byte = 0
	environmentVALUE   byte = 1
	environmentESC     byte = 2
	environmentUSERVAR byte = 3
)

type environmentRequest struct {
	kind byte
	name []byte
}

// environmentResponse implements the USER-only default environment. Explicit
// requests retain their order, including unknown variables (without VALUE),
// duplicates and wildcard expansions. It never reads the process environment.
// An invalid request or an oversized response is ignored as a whole.
func environmentResponse(request []byte, user string, old, reversed bool, limit int) ([]byte, bool) {
	varCode, valueCode := environmentVAR, environmentVALUE
	if old {
		var seenVAR, seenVALUE bool
		for i := 0; i < len(request); i++ {
			switch request[i] {
			case environmentESC:
				i++ // Escaped controls are part of a name, not commands.
			case environmentVAR:
				seenVAR = true
			case environmentVALUE:
				seenVALUE = true
			}
		}
		if seenVAR && seenVALUE {
			return nil, reversed
		}
		// RFC 1571: older BSD peers use the opposite VAR/VALUE codes.
		// Retain the detected convention for later unqualified SENDs.
		if seenVAR || seenVALUE {
			reversed = seenVALUE
		}
		if reversed {
			varCode, valueCode = valueCode, varCode
		}
	}

	requests := make([]environmentRequest, 0, 2)
	for i := 0; i < len(request); i++ {
		value := request[i]
		switch value {
		case varCode, environmentUSERVAR:
			requests = append(requests, environmentRequest{kind: value})
		case valueCode:
			return nil, reversed // SEND contains names, never values.
		default:
			if len(requests) == 0 {
				return nil, reversed
			}
			if value == environmentESC {
				i++
				if i == len(request) {
					return nil, reversed
				}
				value = request[i]
			}
			last := &requests[len(requests)-1]
			last.name = append(last.name, value)
		}
	}
	if len(requests) == 0 {
		requests = append(requests, environmentRequest{kind: varCode})
	}

	response := []byte{environmentIS}
	for _, request := range requests {
		name := string(request.name)
		defined := request.kind == varCode && (name == "USER" || name == "") && user != ""
		if name == "" {
			if !defined {
				continue
			}
			name = "USER"
		}
		size := 1 + escapedEnvironmentLength(name)
		if defined {
			size += 1 + escapedEnvironmentLength(user)
		}
		if size > limit-len(response) {
			return nil, reversed
		}
		response = append(response, request.kind)
		response = appendEscapedEnvironment(response, name)
		if defined {
			response = append(response, valueCode)
			response = appendEscapedEnvironment(response, user)
		}
	}
	return response, reversed
}

func escapedEnvironmentLength(value string) int {
	n := len(value)
	for i := 0; i < len(value); i++ {
		if value[i] <= environmentUSERVAR {
			n++
		}
	}
	return n
}

func appendEscapedEnvironment(dst []byte, value string) []byte {
	for i := 0; i < len(value); i++ {
		if value[i] <= environmentUSERVAR {
			dst = append(dst, environmentESC)
		}
		dst = append(dst, value[i])
	}
	return dst
}
