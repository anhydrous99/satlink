package dns

import (
	"net/netip"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// responseQuestionCount is how many questions a response must have: the one
// of the query it answers.
const responseQuestionCount = 1

// parseResponse returns payload as a DNS message if it is a response to a
// standard query with one question; false, after logging why, otherwise.
func parseResponse(payload []byte) (wire.DNSPacket, bool) {
	response, err := wire.NewDNSPacket(payload)
	switch {
	case err != nil:
		netlog.Trace("dns packet malformed")
	case response.Opcode() != wire.DNSOpcodeQuery:
		netlog.Trace("unwanted opcode", "opcode", response.Opcode())
	case response.Flags()&wire.DNSFlagResponse == 0:
		netlog.Trace("packet doesn't have response bit set")
	case response.QuestionCount() != responseQuestionCount:
		netlog.Trace("bad question count", "count", response.QuestionCount())
	default:
		return response, true
	}
	return nil, false
}

// settle completes the pending query q with the addresses response, which
// matches its port and transaction id, gives for its name, or fails it when
// there are none. It leaves q pending when the response is malformed or
// answers another question.
func (q *query) settle(response wire.DNSPacket) {
	records, ok := q.pending.matchQuestion(response)
	if !ok {
		return
	}
	addresses, ok := q.pending.readAnswers(response, records)
	if !ok {
		return
	}
	if len(addresses) == 0 {
		q.fail()
		return
	}
	q.complete(addresses)
}

// matchQuestion checks that response asks the query's question, and returns
// the records that follow the question.
func (pending *pendingQuery) matchQuestion(response wire.DNSPacket) ([]byte, bool) {
	question, records, err := wire.ParseDNSQuestion(response.Payload())
	if err != nil {
		netlog.Trace("question malformed")
		return nil, false
	}
	if question.Type != pending.queryType {
		netlog.Trace("question type mismatch")
		return nil, false
	}
	equal, err := namesEqual(response.NameLabels(question.Name), response.NameLabels(pending.name))
	switch {
	case err != nil:
		netlog.Trace("dns question name malformed")
	case !equal:
		netlog.Trace("question name mismatch")
	default:
		return records, true
	}
	return nil, false
}

// readAnswers reads the answer records of response, which start records,
// and returns the addresses of those for the query's name, at most
// MaxResultCount; false when a record is malformed.
func (pending *pendingQuery) readAnswers(response wire.DNSPacket, records []byte) ([]netip.Addr, bool) {
	var addresses []netip.Addr
	for range response.AnswerRecordCount() {
		record, rest, err := wire.ParseDNSRecord(records)
		if err != nil {
			netlog.Trace("dns answer record malformed")
			return nil, false
		}
		records = rest
		equal, err := namesEqual(response.NameLabels(record.Name), response.NameLabels(pending.name))
		if err != nil {
			netlog.Trace("dns answer record name malformed")
			return nil, false
		}
		if !equal {
			netlog.Trace("answer name mismatch:", "record", record)
			continue
		}
		if addresses, err = pending.takeRecord(response, record, addresses); err != nil {
			return nil, false
		}
	}
	return addresses, true
}

// takeRecord takes what an answer record for the query's name gives:
// an A record's address, appended to addresses unless they are full, or
// a CNAME's target, which becomes the query's name. It fails with
// wire.ErrMalformed for a target that is malformed or longer than
// MaxNameSize octets, and leaves the query's name as it was.
func (pending *pendingQuery) takeRecord(response wire.DNSPacket, record wire.DNSRecord, addresses []netip.Addr) ([]netip.Addr, error) {
	switch record.Data.Type {
	case wire.DNSTypeA:
		address := record.Data.Address
		netlog.Trace("A:", "addr", address)
		if len(addresses) == MaxResultCount {
			netlog.Trace("too many addresses in response, ignoring", "addr", address)
			return addresses, nil
		}
		return append(addresses, address), nil
	case wire.DNSTypeCNAME:
		netlog.Trace("CNAME:", "name", record.Data.Raw)
		// When faced with a CNAME, recursive resolvers are supposed to
		// resolve the CNAME and append the results for it.
		//
		// We update the query with the new name, so that we pick up the A
		// records for the CNAME when we parse them later. The CNAME's results
		// come after it in the packet, so one linear pass over it is enough.
		if err := copyName(&pending.name, response.NameLabels(record.Data.Raw)); err != nil {
			netlog.Trace("dns answer cname malformed")
			return nil, err
		}
		return addresses, nil
	default:
		netlog.Trace("unknown:", "type", record.Data.Type, "data", record.Data.Raw)
		return addresses, nil
	}
}
