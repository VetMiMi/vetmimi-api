package apperr

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// architectureCodes reads the code and status columns of the "Error model"
// table in docs/architecture.md, the list the site is built against.
func architectureCodes(t *testing.T) map[Code]int {
	t.Helper()
	f, err := os.Open("../../../docs/architecture.md")
	require.NoError(t, err)
	defer f.Close()

	row := regexp.MustCompile("^\\| `([a-z_]+)` \\| (\\d{3}) \\|")
	codes := map[Code]int{}
	inSection := false
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "#") {
			inSection = line == "### Error model"
			continue
		}
		if m := row.FindStringSubmatch(line); inSection && m != nil {
			status, _ := strconv.Atoi(m[2])
			codes[Code(m[1])] = status
		}
	}
	require.NoError(t, s.Err())
	require.NotEmpty(t, codes, "no Error model table found in docs/architecture.md")
	return codes
}

func TestCodesMatchArchitecture(t *testing.T) {
	documented := architectureCodes(t)
	for code, status := range documented {
		k, ok := kinds[code]
		require.Truef(t, ok, "%s is in docs/architecture.md but not in apperr", code)
		require.Equalf(t, status, k.status, "status of %s", code)
	}
	for code := range kinds {
		_, ok := documented[code]
		require.Truef(t, ok, "%s is in apperr but not in docs/architecture.md", code)
	}
}

func TestEveryCodeHasATitle(t *testing.T) {
	for code, k := range kinds {
		require.NotEmptyf(t, k.title, "title of %s", code)
	}
}

func TestUnknownCodeIsAnInternalError(t *testing.T) {
	require.Equal(t, 500, Code("no_such_code").Status())
	require.Equal(t, InternalError.Title(), Code("no_such_code").Title())
}

func TestErrorMessage(t *testing.T) {
	require.Equal(t, "not_found", (&Error{Code: NotFound}).Error())
	require.Equal(t, "not_found: service 7", New(NotFound, "service 7").Error())
}

func TestConstructors(t *testing.T) {
	inv := Invalid("bad body", FieldError{Field: "/email", Message: "required"})
	require.Equal(t, InvalidRequest, inv.Code)
	require.Equal(t, []FieldError{{Field: "/email", Message: "required"}}, inv.Fields)

	rl := RateLimit(3 * time.Second)
	require.Equal(t, RateLimited, rl.Code)
	require.Equal(t, 3*time.Second, rl.RetryAfter)
}
