package textcodec

import (
	"errors"
	"testing"
)

func TestUTF8AndKoreanRoundTrips(t *testing.T) {
	for _, name := range []string{"utf-8", "cp949", "euc-kr"} {
		encoded, err := Encode(name, "설정 검사")
		if err != nil {
			t.Fatalf("Encode(%s): %v", name, err)
		}
		decoded, err := Decode(name, encoded)
		if err != nil {
			t.Fatalf("Decode(%s): %v", name, err)
		}
		if decoded != "설정 검사" {
			t.Fatalf("Decode(%s)=%q", name, decoded)
		}
		ok, err := RoundTrips(name, encoded)
		if err != nil || !ok {
			t.Fatalf("RoundTrips(%s)=%v, %v", name, ok, err)
		}
	}
}

func TestEUCRejectsCP949Extension(t *testing.T) {
	cp949, err := Encode("cp949", "갂")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode("cp949", cp949); err != nil {
		t.Fatalf("CP949 rejected extension: %v", err)
	}
	if _, err := Decode("euc-kr", cp949); !errors.Is(err, ErrInvalid) {
		t.Fatalf("EUC-KR error=%v, want ErrInvalid", err)
	}
}

func TestRejectsInvalidSequence(t *testing.T) {
	for _, name := range []string{"utf-8", "cp949", "euc-kr"} {
		if _, err := Decode(name, []byte{0x81}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Decode(%s) error=%v, want ErrInvalid", name, err)
		}
	}
}
