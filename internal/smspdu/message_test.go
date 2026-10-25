package smspdu

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// withoutServiceCentre returns the PDU of tpdu with the empty service
// centre's address.
func withoutServiceCentre(tpdu []byte) []byte {
	return append([]byte{0}, tpdu...)
}

// decodeHex decodes a PDU written in hexadecimal, as handsets write them.
func decodeHex(t *testing.T, pdu string) Message {
	t.Helper()
	octets, err := hex.DecodeString(pdu)
	if err != nil {
		t.Fatalf("bad test PDU %q: %v", pdu, err)
	}
	message, err := Decode(octets)
	if err != nil {
		t.Fatalf("Decode(%s): %v", pdu, err)
	}
	return message
}

func TestAPublishedSMSDeliverDecodes(t *testing.T) {
	// From service centre +27381000015, sender 27838890001 (a national
	// number), on 99/03/29 at 15:16:59, two hours (eight quarter hours) east
	// of UTC.
	message := decodeHex(t, "07917283010010F5040BC87238880900F10000993092516195800AE8329BFD4697D9EC37")

	want := Message{Kind: KindDeliver, Address: "27838890001", Timestamp: "99/03/29,15:16:59+08", Text: "hellohello"}
	if message != want {
		t.Errorf("Decode = %+v, want %+v", message, want)
	}
}

func TestAPublishedSMSSubmitDecodes(t *testing.T) {
	message := decodeHex(t, "0011000B916407281553F80000AA0AE8329BFD4697D9EC37")

	want := Message{Kind: KindSubmit, Address: "+46708251358", Text: "hellohello"}
	if message != want {
		t.Errorf("Decode = %+v, want %+v", message, want)
	}
}

func TestASubmitIsEncodedAsTheHandsetWritesItsOwn(t *testing.T) {
	// The published example, with a 9555A's week of validity (AD) in place
	// of its four days (AA).
	tpdu, err := EncodeSubmit("+46708251358", "hellohello")

	if err != nil {
		t.Fatalf("EncodeSubmit: %v", err)
	}
	if got, want := strings.ToUpper(hex.EncodeToString(tpdu)), "11000B916407281553F80000AD0AE8329BFD4697D9EC37"; got != want {
		t.Errorf("EncodeSubmit = %s, want %s", got, want)
	}
}

func TestAServiceCentreIsWrittenWithItsLengthInOctets(t *testing.T) {
	tests := map[string]string{"+881662900005": "0791886126090050", "": "00"}
	for number, want := range tests {
		centre, err := EncodeServiceCentre(number)
		if got := strings.ToUpper(hex.EncodeToString(centre)); err != nil || got != want {
			t.Errorf("EncodeServiceCentre(%q) = %s, %v; want %s", number, got, err, want)
		}
	}
}

func TestANumberWithoutAPlusIsNotInternational(t *testing.T) {
	tpdu, err := EncodeSubmit("*99#", "hi")

	if err != nil {
		t.Fatalf("EncodeSubmit: %v", err)
	}
	if got, want := strings.ToUpper(hex.EncodeToString(tpdu[:6])), "110004819AB9"; got != want {
		t.Errorf("EncodeSubmit starts %s, want %s", got, want)
	}
	if message, err := Decode(withoutServiceCentre(tpdu)); err != nil || message.Address != "*99#" {
		t.Errorf("Decode = %+v, %v; want the address *99#", message, err)
	}
}

func TestANumberThatIsNotDialableIsRefused(t *testing.T) {
	for _, number := range []string{"", "+", "+1 555", "12a"} {
		if _, err := EncodeSubmit(number, "hi"); !errors.Is(err, errNotDialable) {
			t.Errorf("EncodeSubmit to %q: %v, want errNotDialable", number, err)
		}
		if _, err := EncodeDeliver(number, "hi", time.Now()); !errors.Is(err, errNotDialable) {
			t.Errorf("EncodeDeliver from %q: %v, want errNotDialable", number, err)
		}
	}
	if _, err := EncodeServiceCentre("+1 555"); !errors.Is(err, errNotDialable) {
		t.Errorf("EncodeServiceCentre: %v, want errNotDialable", err)
	}
}

func TestTextOutsideTheGSMAlphabetGoesAsUCS2(t *testing.T) {
	text := "Привет 🛰"

	tpdu, err := EncodeSubmit("+881631234567", text)

	if err != nil {
		t.Fatalf("EncodeSubmit: %v", err)
	}
	// After the first octet, the reference, a 12-digit address in eight
	// octets and TP-PID: TP-DCS.
	if coding := tpdu[11]; coding != codingUCS2 {
		t.Errorf("TP-DCS %#02x, want UCS-2", coding)
	}
	if message, err := Decode(withoutServiceCentre(tpdu)); err != nil || message.Text != text {
		t.Errorf("Decode = %+v, %v; want %q", message, err, text)
	}
}

func TestOneMessageHoldsAtMost160GSMOr70UCS2Characters(t *testing.T) {
	tests := map[string]bool{
		strings.Repeat("a", 160): true,
		strings.Repeat("a", 161): false,
		strings.Repeat("€", 80):  true, // two septets each
		strings.Repeat("€", 81):  false,
		strings.Repeat("Ж", 70):  true,
		strings.Repeat("Ж", 71):  false,
		strings.Repeat("🛰", 35):  true, // two UTF-16 units each
		strings.Repeat("🛰", 36):  false,
	}
	for text, fits := range tests {
		_, err := EncodeSubmit("+1", text)
		if fits != (err == nil) {
			t.Errorf("EncodeSubmit of %d characters: %v, fits %v", len([]rune(text)), err, fits)
		}
	}
}

func TestADeliverSurvivesARoundTrip(t *testing.T) {
	moment := time.Date(2026, time.October, 22, 9, 5, 7, 0, time.FixedZone("UTC-5", -5*60*60))
	centre, centreErr := EncodeServiceCentre("+881662900005")

	tpdu, err := EncodeDeliver("+881631234567", "Hello from orbit {€}", moment)

	if centreErr != nil || err != nil {
		t.Fatalf("EncodeServiceCentre: %v; EncodeDeliver: %v", centreErr, err)
	}
	message, err := Decode(append(centre, tpdu...))
	want := Message{Kind: KindDeliver, Address: "+881631234567", Timestamp: "26/10/22,09:05:07-20", Text: "Hello from orbit {€}"}
	if err != nil || message != want {
		t.Errorf("Decode = %+v, %v; want %+v", message, err, want)
	}
}

func TestAnAlphanumericSenderReadsAsItsName(t *testing.T) {
	// Type D0, and thirteen semi-octets: the 49 bits of "Iridium", packed
	// in seven octets.
	message := decodeHex(t, "00040DD049799A9CAEB70100006240122201000002E834")

	if message.Address != "Iridium" || message.Text != "hi" {
		t.Errorf("Decode = %+v, want hi from Iridium", message)
	}
}

func TestTheHeaderOfAConcatenatedPartIsSkipped(t *testing.T) {
	// Part 1 of 2 of message CC: a six-octet header, then "hi", which in
	// GSM 7-bit starts after one bit of padding.
	tests := map[string]string{
		"GSM 7-bit": "0044" + "02811100006240122201000009" + "050003CC0201" + "D069",
		"UCS-2":     "0044" + "0281110008624012220100000A" + "050003CC0201" + "00680069",
	}
	for name, pdu := range tests {
		if message := decodeHex(t, pdu); message.Text != "hi" {
			t.Errorf("%s: text %q, want hi", name, message.Text)
		}
	}
}

func TestDataCodingSchemesNameTheirAlphabets(t *testing.T) {
	tests := map[byte]alphabet{
		0x00: alphabetGSM7, 0x04: alphabetEightBit, 0x08: alphabetUCS2, 0x0C: alphabetGSM7,
		0x10: alphabetGSM7, 0x18: alphabetUCS2, 0x24: alphabetEightBit, 0x48: alphabetUCS2,
		0x80: alphabetGSM7, 0xC0: alphabetGSM7, 0xD0: alphabetGSM7, 0xE0: alphabetUCS2,
		0xF0: alphabetGSM7, 0xF4: alphabetEightBit,
	}
	for coding, want := range tests {
		if got := alphabetOf(coding); got != want {
			t.Errorf("alphabetOf(%#02x) = %d, want %d", coding, got, want)
		}
	}
}

func TestEightBitDataReadsAsUTF8(t *testing.T) {
	message := decodeHex(t, "0004028111000462401222010000026869")

	if message.Text != "hi" {
		t.Errorf("text %q, want hi", message.Text)
	}
}

// refusedPDUs are PDUs that do not hold together, by what is wrong with
// them, and the error Decode refuses each with.
var refusedPDUs = map[string]struct {
	pdu  string
	want error
}{
	"empty":                         {"", errTruncated},
	"cut in the service centre":     {"0791886126", errTruncated},
	"cut in the timestamp":          {"00040281110000624012", errTruncated},
	"user data shorter than TP-UDL": {"000402811100006240122201000005E834", errTruncated},
	"a header longer than the data": {"0044028111000462401222010000030A0102", errBadHeader},
	"a GSM 7-bit header too long":   {"0044028111000062401222010000020A01", errBadHeader},
	// TP-UDHI set, TP-DCS UCS-2 and TP-UDL 0: no octet for TP-UDHL.
	"an empty user data that announces a header": {"00440281110008624012220100000000", errBadHeader},
}

func TestAPDUThatDoesNotHoldTogetherIsRefused(t *testing.T) {
	for name, test := range refusedPDUs {
		octets, _ := hex.DecodeString(test.pdu)
		if _, err := Decode(octets); !errors.Is(err, test.want) {
			t.Errorf("%s: %v, want %v", name, err, test.want)
		}
	}
}

// FuzzDecode checks that no PDU makes Decode panic, since anyone may send
// a handset an SMS, and that every string it decodes is valid UTF-8.
func FuzzDecode(f *testing.F) {
	// The published SMS-DELIVER decodes, so that its strings are checked
	// even when the fuzzer does not run.
	published, _ := hex.DecodeString("07917283010010F5040BC87238880900F10000993092516195800AE8329BFD4697D9EC37")
	f.Add(published)
	for _, refused := range refusedPDUs {
		octets, _ := hex.DecodeString(refused.pdu)
		f.Add(octets)
	}
	f.Fuzz(func(t *testing.T, pdu []byte) {
		message, err := Decode(pdu)
		if err != nil {
			return
		}
		for _, text := range []string{message.Address, message.Timestamp, message.Text} {
			if !utf8.ValidString(text) {
				t.Fatalf("% X decodes as %+q", pdu, message)
			}
		}
	})
}

func TestAStatusReportIsNotAMessage(t *testing.T) {
	octets, _ := hex.DecodeString("0002")

	if _, err := Decode(octets); err == nil || !strings.Contains(err.Error(), "TP-MTI 2") {
		t.Errorf("Decode = %v, want TP-MTI 2 refused", err)
	}
}

func TestKindNames(t *testing.T) {
	for kind, want := range map[Kind]string{KindDeliver: "Deliver", KindSubmit: "Submit", Kind(5): "Kind(5)"} {
		if got := kind.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(kind), got, want)
		}
	}
}
