package main

import (
	"cmp"
	"slices"
)

// Suggestions for a mistyped name, such as "tip: a similar subcommand
// exists: 'info'", found by Jaro similarity.

// similarityThreshold is how alike two names must be for one to suggest the
// other: 0.7, so that bar suggests baz.
const similarityThreshold = 0.7

// similarNames returns those of candidates that are like name, the most like
// it last. Candidates alike to the same degree keep their order.
func similarNames(name string, candidates []string) []string {
	type scored struct {
		name       string
		similarity float64
	}
	var similar []scored
	for _, candidate := range candidates {
		if similarity := jaro(name, candidate); similarity > similarityThreshold {
			similar = append(similar, scored{name: candidate, similarity: similarity})
		}
	}
	slices.SortStableFunc(similar, func(a, b scored) int {
		return cmp.Compare(a.similarity, b.similarity)
	})
	names := make([]string, len(similar))
	for i, candidate := range similar {
		names[i] = candidate.name
	}
	return names
}

// mostSimilarName returns the one of candidates most like name, and false
// when none is like it.
func mostSimilarName(name string, candidates []string) (string, bool) {
	similar := similarNames(name, candidates)
	if len(similar) == 0 {
		return "", false
	}
	return similar[len(similar)-1], true
}

// jaro returns the Jaro similarity of a and b, from 0 for nothing in common
// to 1 for the same text, counted over their characters rather than their
// bytes.
func jaro(a, b string) float64 {
	first, second := []rune(a), []rune(b)
	switch {
	case len(first) == 0 && len(second) == 0:
		return 1
	case len(first) == 0 || len(second) == 0:
		return 0
	}
	firstMatched, secondMatched := matchCharacters(first, second)
	matches := 0
	for _, matched := range firstMatched {
		if matched {
			matches++
		}
	}
	if matches == 0 {
		return 0
	}
	transpositions := countTranspositions(first, second, firstMatched, secondMatched) / 2
	m := float64(matches)
	return (m/float64(len(first)) + m/float64(len(second)) + (m-float64(transpositions))/m) / 3
}

// matchCharacters finds the characters that a and b have in common within
// Jaro's window — half the longer length, less one — each matched once, and
// returns which of each text's characters matched.
func matchCharacters(a, b []rune) (aMatched, bMatched []bool) {
	window := max(max(len(a), len(b))/2-1, 0)
	aMatched, bMatched = make([]bool, len(a)), make([]bool, len(b))
	for i, character := range a {
		for j := max(i-window, 0); j < min(len(b), i+window+1); j++ {
			if !bMatched[j] && b[j] == character {
				aMatched[i], bMatched[j] = true, true
				break
			}
		}
	}
	return aMatched, bMatched
}

// countTranspositions counts the matched characters that are not in the same
// order in a and b; each transposition counts twice.
func countTranspositions(a, b []rune, aMatched, bMatched []bool) int {
	transpositions, j := 0, 0
	for i, character := range a {
		if !aMatched[i] {
			continue
		}
		for !bMatched[j] {
			j++
		}
		if b[j] != character {
			transpositions++
		}
		j++
	}
	return transpositions
}

// Tokens that the command does not take, and what to suggest instead. An
// option waiting for its value is not reported then: the token that cannot
// be placed is.

// unknownLong explains --name, an option the command does not take. It
// suggests the command's option most like it, or one that a subcommand named
// later in the command line, rest, takes; or, for a command that takes
// positional arguments, how to give the token as a value.
func (e explainer) unknownLong(lvl *level, name string, rest []string) *commandLineExit {
	cmd, token := lvl.command, "--"+name
	used := slices.Clone(lvl.given)
	var tips []string
	if similar, found := mostSimilarName(name, cmd.longNames()); found {
		tips = append(tips, e.similarTip("argument", []string{"--" + similar}))
		if option := cmd.option(similar); !slices.Contains(used, option) {
			used = append(used, option)
		}
	} else if subcommand, similar, found := subcommandOptionLike(cmd, name, rest); found {
		tips = append(tips, "'"+e.palette.valid(subcommand+" --"+similar)+"' exists")
	} else if len(cmd.positionals()) > 0 {
		tips = append(tips, e.valueTip(token))
	}
	return e.unexpectedArgument(cmd, token, tips, used)
}

// subcommandOptionLike returns the subcommand of cmd that rest names first
// among those taking an option like --name, and that option's name.
func subcommandOptionLike(cmd *command, name string, rest []string) (subcommand, option string, found bool) {
	first := len(rest)
	for _, candidate := range cmd.subcommands {
		similar, alike := mostSimilarName(name, candidate.longNames())
		position := slices.Index(rest, candidate.name)
		if alike && position >= 0 && position < first {
			first, subcommand, option, found = position, candidate.name, similar, true
		}
	}
	return subcommand, option, found
}

// unknownShort explains token, such as -x, an option the command does not
// take: a command that takes positional arguments may have been given a
// value such as -5.
func (e explainer) unknownShort(lvl *level, token string) *commandLineExit {
	var tips []string
	if len(lvl.command.positionals()) > 0 {
		tips = append(tips, e.valueTip(token))
	}
	return e.unexpectedArgument(lvl.command, token, tips, nil)
}

// notAnArgument explains token, a value the command has no room for: a
// subcommand's name after "--", a subcommand mistyped, or a value too many.
func (e explainer) notAnArgument(lvl *level, token string) *commandLineExit {
	cmd := lvl.command
	if lvl.trailing && cmd.subcommand(token) != nil {
		tip := "subcommand '" + e.palette.valid(token) + "' exists; to use it, remove the '" +
			e.palette.invalid("--") + "' before it"
		return e.unexpectedArgument(cmd, token, []string{tip}, nil)
	}
	if similar := similarNames(token, cmd.subcommandNames()); len(similar) > 0 {
		return e.unrecognizedSubcommand(cmd, token, []string{e.similarTip("subcommand", similar)})
	}
	if cmd.hasSubcommands() && len(cmd.positionals()) == 0 {
		return e.unrecognizedSubcommand(cmd, token, nil)
	}
	return e.unexpectedArgument(cmd, token, nil, nil)
}

// valueTip tells how to give token, which looks like an option, as a value.
func (e explainer) valueTip(token string) string {
	return "to pass '" + e.palette.invalid(token) + "' as a value, use '" + e.palette.valid("-- "+token) + "'"
}
