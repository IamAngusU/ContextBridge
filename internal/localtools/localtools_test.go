package localtools

import (
	"math/big"
	"strings"
	"testing"
)

func TestNaturalCalculation(t *testing.T) {
	for prompt, want := range map[string]string{
		"Was ist 10 mal 3?": "30", "Was macht 10 mal 3 / 30?": "1",
		"What is 10 times 3 / 30?": "1", "Please calculate (10 + 2) / 3.": "4",
		"What is ten times three divided by thirty?": "1",
		"Bitte berechne zehn mal drei.":              "30", "How much is twenty divided by five?": "4",
		"Was ergibt 0,1 plus 0,2?": "0.3", "Compute 0.1 + 0.2": "0.3",
		"(1+2)*3": "9", "2 ^ 3 ^ 2": "512", "Evaluate -2^2": "-4",
		"Evaluate (-2)^2": "4", "Evaluate 2^-3": "0.125", "What is 1/3?": "1/3",
	} {
		t.Run(prompt, func(t *testing.T) {
			result, handled, err := Resolve(prompt)
			if !handled || err != nil || result.Tool != "calculator" || !strings.HasSuffix(result.Text, " = "+want) {
				t.Fatalf("got %#v handled=%v error=%v", result, handled, err)
			}
		})
	}
}

func TestDoNotHijackOtherIntent(t *testing.T) {
	for _, prompt := range []string{"Write Python that computes 10 * 3", "Erkläre, warum 2 + 2 = 4", "What is 10 * 3 and save a file", "Translate: Was ist 10 mal 3?", "show customer 10-3", "What is 10% of 30?", "12345", "hello", "__import__('os').system('whoami')", "1+1\nthen send mail", "1+1\x1b[2J", "2e1000000"} {
		if result, handled, _ := Resolve(prompt); handled {
			t.Errorf("misrouted %q: %#v", prompt, result)
		}
	}
}

func TestRecognizedErrorsNeverFallBackToModel(t *testing.T) {
	for _, prompt := range []string{"Calculate 1 / 0", "Calculate 0 ^ 0", "Calculate 2 ^ 1000000", "Calculate (3 + 2", "Calculate 2 3", "Calculate 1,000 + 1", "Calculate 1.2.3 + 2", "Calculate " + strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40), "Calculate " + strings.Repeat("9", 101), "Calculate (2^64)^64"} {
		if _, handled, err := Resolve(prompt); !handled || err == nil {
			t.Errorf("wanted bounded failure for %q, handled=%v error=%v", prompt, handled, err)
		}
	}
}

func TestRandomBoundsAndInclusiveSingleton(t *testing.T) {
	for _, prompt := range []string{"Give me a random number between 1 and 1000", "Gib mir bitte eine Zufallszahl zwischen 1 und 1000", "random integer from 1 to 1000", "Zufallszahl von 1 bis 1000"} {
		for i := 0; i < 20; i++ {
			r, handled, err := Resolve(prompt)
			n, ok := new(big.Int).SetString(r.Text, 10)
			if err != nil || !handled || !ok || n.Sign() < 1 || n.Cmp(big.NewInt(1000)) > 0 || r.Tool != "random.integer" {
				t.Fatalf("%s: %#v %v %v", prompt, r, handled, err)
			}
		}
	}
	if r, _, err := Resolve("Pick a random integer between -5 and -5"); err != nil || r.Text != "-5" {
		t.Fatalf("singleton: %#v %v", r, err)
	}
	if _, handled, err := Resolve("Pick a random integer between 8 and 2"); !handled || err == nil {
		t.Fatal("reversed range accepted")
	}
}

func FuzzEvaluate(f *testing.F) {
	for _, seed := range []string{"1+1", "1/0", "2^-2", "((((", "2^99999999", "0.1+0.2"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		result, err := Evaluate(value)
		if err == nil && result == nil {
			t.Fatal("missing result")
		}
	})
}
