package main

import "strconv"

// What the command line gives an argument, kept where the command that
// takes it reads it.

// argumentValue takes the text the command line gives an argument — once
// for each time it is given — and keeps what it means, or refuses it with
// the reason.
type argumentValue interface {
	Set(text string) error
}

// stringValue keeps the text given.
type stringValue struct {
	target *string
}

// Set keeps text.
func (v stringValue) Set(text string) error {
	*v.target = text
	return nil
}

// givenText is text that may not have been given at all, like an optional
// string: given tells an empty text from none.
type givenText struct {
	text  string
	given bool
}

// Set keeps text, and that it was given.
func (v *givenText) Set(text string) error {
	v.text, v.given = text, true
	return nil
}

// stringsValue keeps every text given, in order.
type stringsValue struct {
	target *[]string
}

// Set adds text to those given before.
func (v stringsValue) Set(text string) error {
	*v.target = append(*v.target, text)
	return nil
}

// countValue keeps how often a flag was given, which the parser passes in
// decimal.
type countValue struct {
	target *int
}

// Set keeps the count that text gives.
func (v countValue) Set(text string) error {
	count, err := strconv.Atoi(text)
	if err != nil {
		return err
	}
	*v.target = count
	return nil
}
