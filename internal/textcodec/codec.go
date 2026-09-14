// Package textcodec decodes explicitly selected workspace text encodings.
package textcodec

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/transform"
)

var (
	ErrUnsupported = errors.New("text codec: unsupported encoding")
	ErrInvalid     = errors.New("text codec: invalid byte sequence")
)

func Normalize(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "utf-8", "utf8":
		return "utf-8", nil
	case "cp949", "windows-949", "uhc":
		return "cp949", nil
	case "euc-kr", "euckr":
		return "euc-kr", nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupported, name)
	}
}

func Decode(name string, input []byte) (string, error) {
	normalized, err := Normalize(name)
	if err != nil {
		return "", err
	}
	switch normalized {
	case "utf-8":
		input = bytes.TrimPrefix(input, []byte{0xEF, 0xBB, 0xBF})
		if !utf8.Valid(input) {
			return "", ErrInvalid
		}
		return string(input), nil
	case "euc-kr":
		if err := validateEUCStrict(input); err != nil {
			return "", err
		}
	case "cp949":
		if err := validateCP949(input); err != nil {
			return "", err
		}
	}
	decoded, _, err := transform.Bytes(korean.EUCKR.NewDecoder(), input)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !utf8.Valid(decoded) {
		return "", ErrInvalid
	}
	return string(decoded), nil
}

func Encode(name, input string) ([]byte, error) {
	normalized, err := Normalize(name)
	if err != nil {
		return nil, err
	}
	if normalized == "utf-8" {
		return []byte(input), nil
	}
	encoded, _, err := transform.String(korean.EUCKR.NewEncoder(), input)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", normalized, err)
	}
	result := []byte(encoded)
	if normalized == "euc-kr" {
		if err := validateEUCStrict(result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func RoundTrips(name string, original []byte) (bool, error) {
	decoded, err := Decode(name, original)
	if err != nil {
		return false, err
	}
	encoded, err := Encode(name, decoded)
	if err != nil {
		return false, err
	}
	return bytes.Equal(original, encoded), nil
}

func validateEUCStrict(input []byte) error {
	for offset := 0; offset < len(input); {
		if input[offset] <= 0x7F {
			offset++
			continue
		}
		if offset+1 >= len(input) || input[offset] < 0xA1 || input[offset] > 0xFE || input[offset+1] < 0xA1 || input[offset+1] > 0xFE {
			return fmt.Errorf("%w at byte %d", ErrInvalid, offset)
		}
		offset += 2
	}
	return nil
}

func validateCP949(input []byte) error {
	for offset := 0; offset < len(input); {
		lead := input[offset]
		if lead <= 0x7F {
			offset++
			continue
		}
		if offset+1 >= len(input) || lead < 0x81 || lead > 0xFE || !validCP949Trail(input[offset+1]) {
			return fmt.Errorf("%w at byte %d", ErrInvalid, offset)
		}
		offset += 2
	}
	return nil
}

func validCP949Trail(value byte) bool {
	return value >= 0x41 && value <= 0x5A || value >= 0x61 && value <= 0x7A || value >= 0x81 && value <= 0xFE
}
