// Package localtools resolves a deliberately small set of unambiguous natural
// language requests without a model, network, filesystem or shell. Recognition
// must cover the entire request; embedded examples and compound tasks are not
// silently reduced to a calculation. Callers retain control over when to use it.
package localtools

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

type Result struct {
	Tool  string `json:"tool"`
	Input string `json:"input"`
	Text  string `json:"text"`
}

var randomRequest = regexp.MustCompile(`^(?:(?:please )?(?:give me|pick|choose|generate|draw) |(?:gib mir|wähle|waehle|ziehe|generiere) (?:bitte )?)?(?:a |an |eine )?(?:random (?:integer|number)|zufallszahl)(?: (?:between|from|zwischen|von)) (-?[0-9]{1,18}) (?:and|to|und|bis|bis einschließlich) (-?[0-9]{1,18})(?: bitte| please)?$`)
var word = regexp.MustCompile(`[\pL]+`)
var numericExpression = regexp.MustCompile(`^[0-9.,+*/^()\s-]+$`)
var words = map[string]string{
	"plus": "+", "minus": "-", "mal": "*", "times": "*", "durch": "/", "hoch": "^",
	"null": "0", "zero": "0", "eins": "1", "one": "1", "zwei": "2", "two": "2",
	"drei": "3", "three": "3", "vier": "4", "four": "4", "fünf": "5", "fuenf": "5", "five": "5",
	"sechs": "6", "six": "6", "sieben": "7", "seven": "7", "acht": "8", "eight": "8",
	"neun": "9", "nine": "9", "zehn": "10", "ten": "10", "elf": "11", "eleven": "11",
	"zwölf": "12", "zwoelf": "12", "twelve": "12", "dreizehn": "13", "thirteen": "13",
	"vierzehn": "14", "fourteen": "14", "fünfzehn": "15", "fifteen": "15", "sechzehn": "16", "sixteen": "16",
	"siebzehn": "17", "seventeen": "17", "achtzehn": "18", "eighteen": "18", "neunzehn": "19", "nineteen": "19",
	"zwanzig": "20", "twenty": "20", "hundert": "100", "hundred": "100", "tausend": "1000", "thousand": "1000",
}

// Resolve returns handled=true even for invalid recognized arithmetic, so a
// division by zero or exhausted numeric budget never falls back to an LLM guess.
func Resolve(prompt string) (result Result, handled bool, err error) {
	if len(prompt) > 4096 || strings.ContainsAny(prompt, "\r\n\x00") {
		return Result{}, false, nil
	}
	text := strings.ToLower(strings.TrimSpace(prompt))
	text = strings.TrimSpace(strings.TrimRight(text, "?!"))
	text = strings.TrimSpace(strings.TrimSuffix(text, "."))
	if match := randomRequest.FindStringSubmatch(text); match != nil {
		lower, _ := new(big.Int).SetString(match[1], 10)
		upper, _ := new(big.Int).SetString(match[2], 10)
		result = Result{Tool: "random.integer", Input: match[1] + ".." + match[2]}
		if lower.Cmp(upper) > 0 {
			return result, true, errors.New("random.integer: lower bound must not exceed upper bound")
		}
		width := new(big.Int).Sub(upper, lower)
		width.Add(width, big.NewInt(1))
		value, err := rand.Int(rand.Reader, width)
		if err != nil {
			return result, true, errors.New("random.integer: operating-system randomness unavailable")
		}
		result.Text = value.Add(value, lower).String()
		return result, true, nil
	}
	text = strings.TrimPrefix(text, "please ")
	text = strings.TrimPrefix(text, "bitte ")
	explicit, german := false, false
	for _, prefix := range []string{"kannst du bitte berechnen ", "kannst du berechnen ", "was ergibt ", "wie viel ist ", "wieviel ist ", "was macht ", "was ist ", "berechne bitte ", "berechne ", "rechne bitte ", "rechne ", "what is ", "what's ", "how much is ", "calculate ", "compute ", "evaluate "} {
		if strings.HasPrefix(text, prefix) {
			text = strings.TrimPrefix(text, prefix)
			explicit = true
			german = !strings.HasPrefix(prefix, "what") && !strings.HasPrefix(prefix, "how") && prefix != "calculate " && prefix != "compute " && prefix != "evaluate "
			break
		}
	}
	text = strings.TrimSuffix(strings.TrimSuffix(text, " please"), " bitte")
	text = strings.NewReplacer("multiplied by", "*", "divided by", "/", "geteilt durch", "/", "multipliziert mit", "*", "to the power of", "^", "×", "*", "÷", "/", "−", "-", "**", "^").Replace(text)
	text = word.ReplaceAllStringFunc(text, func(token string) string {
		if replacement, ok := words[token]; ok {
			return replacement
		}
		return token
	})
	if !numericExpression.MatchString(text) || (!explicit && !strings.ContainsAny(text, "+-*/^")) {
		return Result{}, false, nil
	}
	result = Result{Tool: "calculator", Input: strings.TrimSpace(text)}
	if strings.Contains(text, ",") {
		// Commas without a German request are ambiguous (decimal vs thousands).
		if !german || strings.Contains(text, ".") {
			return result, true, errors.New("calculator: ambiguous decimal separators; use decimal points without thousands separators")
		}
		text = strings.ReplaceAll(text, ",", ".")
	}
	value, err := Evaluate(text)
	if err != nil {
		return result, true, fmt.Errorf("calculator: %w", err)
	}
	result.Text = result.Input + " = " + format(value)
	return result, true, nil
}

// Evaluate is a bounded rational-arithmetic parser, never eval or a subprocess.
// Supported operators: + - * / ^ and parentheses; powers are integer -64..64.
func Evaluate(expression string) (*big.Rat, error) {
	if len(expression) > 1024 {
		return nil, errors.New("expression exceeds 1024 bytes")
	}
	p := parser{text: expression}
	value, err := p.expression(0, 0)
	p.space()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.text) {
		return nil, errors.New("unexpected token; use explicit arithmetic operators")
	}
	return value, nil
}

type parser struct {
	text        string
	pos, tokens int
}

func (p *parser) space() {
	for p.pos < len(p.text) && (p.text[p.pos] == ' ' || p.text[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) expression(minimum, depth int) (*big.Rat, error) {
	p.tokens++
	if p.tokens > 128 || depth > 32 {
		return nil, errors.New("expression complexity limit exceeded")
	}
	p.space()
	if p.pos == len(p.text) {
		return nil, errors.New("missing number")
	}
	var left *big.Rat
	var err error
	switch p.text[p.pos] {
	case '+', '-':
		sign := p.text[p.pos]
		p.pos++
		left, err = p.expression(3, depth+1)
		if err == nil && sign == '-' {
			left.Neg(left)
		}
	case '(':
		p.pos++
		left, err = p.expression(0, depth+1)
		p.space()
		if err == nil && (p.pos == len(p.text) || p.text[p.pos] != ')') {
			err = errors.New("missing closing parenthesis")
		}
		if err == nil {
			p.pos++
		}
	default:
		start := p.pos
		for p.pos < len(p.text) && (p.text[p.pos] >= '0' && p.text[p.pos] <= '9' || p.text[p.pos] == '.') {
			p.pos++
		}
		literal := p.text[start:p.pos]
		if len(literal) == 0 || len(literal) > 100 {
			return nil, errors.New("expected number with at most 100 digits")
		}
		var ok bool
		left, ok = new(big.Rat).SetString(literal)
		if !ok {
			return nil, errors.New("invalid decimal number")
		}
	}
	if err != nil {
		return nil, err
	}
	for {
		p.space()
		if p.pos == len(p.text) {
			break
		}
		op := p.text[p.pos]
		priority := 0
		switch op {
		case '+', '-':
			priority = 1
		case '*', '/':
			priority = 2
		case '^':
			priority = 3
		}
		if priority == 0 || priority < minimum {
			break
		}
		p.pos++
		next := priority + 1
		if op == '^' {
			next = priority
		}
		right, err := p.expression(next, depth+1)
		if err != nil {
			return nil, err
		}
		switch op {
		case '+':
			left.Add(left, right)
		case '-':
			left.Sub(left, right)
		case '*':
			left.Mul(left, right)
		case '/':
			if right.Sign() == 0 {
				return nil, errors.New("division by zero")
			}
			left.Quo(left, right)
		case '^':
			if !right.IsInt() || !right.Num().IsInt64() || right.Num().Int64() < -64 || right.Num().Int64() > 64 {
				return nil, errors.New("power must be an integer between -64 and 64")
			}
			power := right.Num().Int64()
			if left.Sign() == 0 && power <= 0 {
				return nil, errors.New("undefined zero power")
			}
			if power < 0 {
				left.Inv(left)
				power = -power
			}
			if left.Num().BitLen()*int(power) > 4096 || left.Denom().BitLen()*int(power) > 4096 {
				return nil, errors.New("numeric size limit exceeded")
			}
			left.SetFrac(new(big.Int).Exp(left.Num(), big.NewInt(power), nil), new(big.Int).Exp(left.Denom(), big.NewInt(power), nil))
		}
		if left.Num().BitLen() > 4096 || left.Denom().BitLen() > 4096 {
			return nil, errors.New("numeric size limit exceeded")
		}
	}
	return left, nil
}

func format(value *big.Rat) string {
	if value.IsInt() {
		return value.Num().String()
	}
	// Keep repeating or long decimals exact instead of silently rounding.
	for places := 1; places <= 12; places++ {
		decimal := value.FloatString(places)
		roundtrip, _ := new(big.Rat).SetString(decimal)
		if roundtrip.Cmp(value) == 0 {
			return decimal
		}
	}
	return value.RatString()
}

// TerminalSafe is shared by result/confirmation UIs: control and direction
// overrides are escaped rather than permitted to alter the terminal display.
func TerminalSafe(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf)) {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
