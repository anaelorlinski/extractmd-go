// Package wordnum computes Word list and heading numbers ("2.1.3", "a)", "-") the way Word displays them.
// It is shared by the .docx (WordprocessingML) and .doc (binary) readers, which only differ in how they
// load list definitions.
package wordnum

import (
	"fmt"
	"strconv"
	"strings"
)

// Level is one level of a list definition. Text uses %1..%9 placeholders for the level numbers.
type Level struct {
	Format string // decimal, lowerLetter, upperLetter, lowerRoman, upperRoman, decimalZero, bullet, none
	Text   string
	Start  int
}

// Numbering holds list definitions (abstract lists), list instances and the running counters.
type Numbering struct {
	Abstracts map[string]map[int]*Level // abstract list id -> level -> definition
	NumToAbs  map[string]string         // list instance id -> abstract list id
	Starts    map[string]map[int]int    // start overrides per list instance and level

	counters map[string]map[int]int // per abstract list: Word shares counters across instances of the same definition
	seenNums map[string]bool
}

func New() *Numbering {
	return &Numbering{
		Abstracts: map[string]map[int]*Level{},
		NumToAbs:  map[string]string{},
		Starts:    map[string]map[int]int{},
		counters:  map[string]map[int]int{},
		seenNums:  map[string]bool{},
	}
}

// SetStart records a start override for a list instance level.
func (n *Numbering) SetStart(numID string, ilvl, start int) {
	if n.Starts[numID] == nil {
		n.Starts[numID] = map[int]int{}
	}
	n.Starts[numID][ilvl] = start
}

// Next advances the counters for a paragraph of list instance numID at level ilvl and returns its label
// ("-" for bullets, "" for unnumbered levels). ok is false when the list or level is not defined.
func (n *Numbering) Next(numID string, ilvl int) (label string, ok bool) {
	absID := n.NumToAbs[numID]
	levels := n.Abstracts[absID]
	cur := levels[ilvl]
	if cur == nil {
		return "", false
	}
	if n.counters[absID] == nil {
		n.counters[absID] = map[int]int{}
	}
	cnt := n.counters[absID]
	if !n.seenNums[numID] {
		n.seenNums[numID] = true
		// A start override restarts the paragraph's level and deeper; parents already counting keep their value
		for k, v := range n.Starts[numID] {
			_, counting := cnt[k]
			switch {
			case k >= ilvl:
				cnt[k] = v - 1
			case !counting:
				cnt[k] = v // an unused parent is displayed at its start value
			}
		}
	}
	if _, seen := cnt[ilvl]; !seen {
		cnt[ilvl] = n.levelStart(numID, ilvl, cur) - 1
	}
	// Like Word, an unused parent level is displayed at its start value and counts as used
	for k := 0; k < ilvl; k++ {
		if _, seen := cnt[k]; !seen && levels[k] != nil {
			cnt[k] = n.levelStart(numID, k, levels[k])
		}
	}
	cnt[ilvl]++
	for k := range cnt {
		if k > ilvl {
			delete(cnt, k)
		}
	}
	switch cur.Format {
	case "bullet":
		return "-", true
	case "none":
		return "", true
	}
	label = cur.Text
	for k := 0; k <= ilvl; k++ {
		ph := "%" + strconv.Itoa(k+1)
		if !strings.Contains(label, ph) {
			continue
		}
		lv := levels[k]
		v, seen := cnt[k]
		if !seen && lv != nil {
			v = n.levelStart(numID, k, lv)
		}
		f := "decimal"
		if lv != nil {
			f = lv.Format
		}
		label = strings.ReplaceAll(label, ph, FormatNumber(v, f))
	}
	return strings.TrimSpace(label), true
}

func (n *Numbering) levelStart(numID string, ilvl int, lv *Level) int {
	if s, ok := n.Starts[numID][ilvl]; ok {
		return s
	}
	return lv.Start
}

// FormatNumber renders a counter value in a Word number format.
func FormatNumber(n int, format string) string {
	switch format {
	case "lowerLetter", "upperLetter":
		s := ""
		for x := n; x > 0; x = (x - 1) / 26 {
			s = string(rune('a'+(x-1)%26)) + s
		}
		if format == "upperLetter" {
			return strings.ToUpper(s)
		}
		return s
	case "lowerRoman", "upperRoman":
		s := toRoman(n)
		if format == "lowerRoman" {
			return strings.ToLower(s)
		}
		return s
	case "decimalZero":
		return fmt.Sprintf("%02d", n)
	}
	return strconv.Itoa(n)
}

func toRoman(n int) string {
	vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	syms := []string{"M", "CM", "D", "CD", "C", "XC", "L", "XL", "X", "IX", "V", "IV", "I"}
	var sb strings.Builder
	for i, v := range vals {
		for n >= v {
			sb.WriteString(syms[i])
			n -= v
		}
	}
	return sb.String()
}
