// Package smspdu encodes and decodes SMS in PDU mode, the only mode
// Iridium handsets have (ISU AT Command Reference, +CMGF): the SMS-SUBMIT
// a handset is given to send, and the SMS-DELIVER and SMS-SUBMIT it stores
// (3GPP TS 23.040 §9.2, TS 23.038). Package iridium sends and reads
// messages with it, and package sim serves them.
//
// A PDU here is what +CMGS takes and +CMGL and +CMGR give: the service
// centre's address, then the TPDU. The codec is sans-I/O and keeps to one
// message at a time. A concatenated message is decoded part by part, its
// header skipped, and text longer than one message holds is refused.
package smspdu
