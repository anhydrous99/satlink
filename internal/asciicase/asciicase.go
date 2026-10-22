package asciicase

// caseOffset is the distance between an ASCII capital letter and its small
// form.
const caseOffset = 'a' - 'A'

// EqualFold reports whether a and b are the same but for the case of ASCII
// letters, as Rust's eq_ignore_ascii_case compares them.
func EqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lower(a[i]) != lower(b[i]) {
			return false
		}
	}
	return true
}

// Lower returns text with its ASCII capital letters in small case, as
// Rust's to_ascii_lowercase does.
func Lower(text string) string {
	return mapBytes(text, lower)
}

// Upper returns text with its ASCII small letters in capitals, as Rust's
// to_ascii_uppercase does.
func Upper(text string) string {
	return mapBytes(text, upper)
}

// mapBytes returns text with mapping applied to each of its bytes.
func mapBytes(text string, mapping func(byte) byte) string {
	mapped := []byte(text)
	for i, b := range mapped {
		mapped[i] = mapping(b)
	}
	return string(mapped)
}

// lower returns b in small case if it is an ASCII capital letter, and
// unchanged otherwise.
func lower(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + caseOffset
	}
	return b
}

// upper returns b as a capital if it is an ASCII small letter, and
// unchanged otherwise.
func upper(b byte) byte {
	if 'a' <= b && b <= 'z' {
		return b - caseOffset
	}
	return b
}
