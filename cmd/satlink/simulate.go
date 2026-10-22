package main

import (
	"errors"

	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/sim"
)

// The --simulate option: a simulated handset instead of a real one, with
// Iridium's real timing or instant responses.

// linkProfile is a satellite link that --simulate offers, by name.
type linkProfile struct {
	name string
	// help says what the link is like, as --simulate's help shows it.
	help    string
	profile func() sim.LinkProfile
}

// defaultLinkProfile is what --simulate without a value chooses.
const defaultLinkProfile = "iridium"

// linkProfiles returns the links that --simulate offers.
func linkProfiles() []linkProfile {
	return []linkProfile{
		{name: defaultLinkProfile, help: "Call setup, latency and throughput as measured on Iridium",
			profile: sim.IridiumLinkProfile},
		{name: "instant", help: "No delays: for quick experiments", profile: sim.InstantLinkProfile},
	}
}

// linkProfileValues returns the values that --simulate takes, and what they
// mean.
func linkProfileValues() []possibleValue {
	var values []possibleValue
	for _, profile := range linkProfiles() {
		values = append(values, possibleValue{name: profile.name, help: profile.help})
	}
	return values
}

// simulation is the value of --simulate: the link of the simulated handset
// to use instead of a real one, if any. The zero simulation asks for none.
type simulation struct {
	chosen *linkProfile
}

// Set chooses the link profile named name. The command line gives it only
// the names linkProfileValues lists (parser.check): the refusal keeps the
// contract of argumentValue for any other caller.
func (s *simulation) Set(name string) error {
	for _, profile := range linkProfiles() {
		if profile.name == name {
			s.chosen = &profile
			return nil
		}
	}
	return errors.New(debugtext.Quote(name) + " is not a simulated link")
}

// config returns the simulated handset's configuration — the default one,
// with the chosen link — and false when no simulation was asked for.
func (s *simulation) config() (sim.Config, bool) {
	if s.chosen == nil {
		return sim.Config{}, false
	}
	config := sim.DefaultConfig()
	config.Link = s.chosen.profile()
	return config, true
}
