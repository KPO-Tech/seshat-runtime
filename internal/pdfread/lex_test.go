package pdf

import (
	"strings"
	"testing"
	"time"
)

// A content stream that ends inside an array must end the array, not loop adding end-of-data markers until the memory is gone.
func TestAnArrayThatIsNeverClosedEndsWithTheData(t *testing.T) {
	for name, src := range map[string]string{
		"open array":         "[1 2 3",
		"nested open array":  "[1 [2 3",
		"array in a comment": "[1 2 % end",
		"closed array":       "[1 2 3] 4",
	} {
		done := make(chan array, 1)
		go func() {
			b := newBuffer(strings.NewReader(src), 0)
			b.allowEOF = true
			got, _ := b.readObject().(array)
			done <- got
		}()
		select {
		case got := <-done:
			if len(got) == 0 || len(got) > 4 {
				t.Errorf("%s: read %d elements, want a few", name, len(got))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: reading did not stop at the end of the data", name)
		}
	}
}
