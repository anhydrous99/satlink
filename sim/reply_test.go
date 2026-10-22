package sim

import "testing"

func TestRepliesAreFramedLikeAHandsetFramesThem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		reply reply
		want  string
	}{
		{name: "OK", reply: okReply(), want: "\r\nOK\r\n"},
		{name: "information", reply: okReply("+CSQ:4"), want: "\r\n+CSQ:4\r\n\r\nOK\r\n"},
		{name: "several lines", reply: okReply("+CMGR: x", "text"), want: "\r\n+CMGR: x\r\ntext\r\n\r\nOK\r\n"},
		{name: "ERROR", reply: reply{kind: replyError}, want: "\r\nERROR\r\n"},
		{name: "SMS error", reply: smsErrorReply(smsErrorInvalidMemoryIndex), want: "\r\n+CMS ERROR: 321\r\n"},
		{name: "NO CARRIER", reply: reply{kind: replyNoCarrier}, want: "\r\nNO CARRIER\r\n"},
		{name: "CONNECT", reply: reply{kind: replyConnect}, want: "\r\nCONNECT 19200\r\n"},
		{name: "text prompt", reply: reply{kind: replyTextPrompt}, want: "\r\n> "},
	}

	for _, test := range cases {
		if got := test.reply.encode(); got != test.want {
			t.Errorf("%s: %q; want %q", test.name, got, test.want)
		}
	}
	if got := unsolicited("+CMTI: \"SM\",1"); got != "\r\n+CMTI: \"SM\",1\r\n" {
		t.Errorf("unsolicited: %q", got)
	}
}
