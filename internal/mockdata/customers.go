package mockdata

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// The name pools are intentionally diverse and invented for this project; any
// resemblance to a specific real person is coincidental.
var firstNames = []string{
	"Aaliyah", "Mateo", "Priya", "Liam", "Sofia", "Kenji", "Amara", "Noah",
	"Isabella", "Wei", "Fatima", "Ethan", "Olivia", "Diego", "Ava", "Rohan",
	"Mia", "Hiroshi", "Zara", "Lucas", "Emma", "Omar", "Chloe", "Santiago",
	"Layla", "Elena", "Marcus", "Nadia", "Jasper", "Ines", "Tobias", "Yuki",
	"Camila", "Andre", "Leila", "Finn", "Grace", "Hassan", "Bianca", "Dmitri",
	"Aisha", "Caleb", "Freya", "Rafael", "Naomi", "Theo", "Anaya", "Julian",
	"Simone", "Mateus",
}

var lastNames = []string{
	"Nguyen", "Patel", "Okafor", "Garcia", "Kim", "Rossi", "Andersson", "Haddad",
	"Silva", "Cohen", "Mueller", "Tanaka", "Johnson", "Fernandez", "OBrien",
	"Kowalski", "Mensah", "Reyes", "Novak", "Ivanov", "Santos", "Khan", "Bauer",
	"Dubois", "Costa", "Larsen", "Yamamoto", "Adebayo", "Romano", "Petrov",
	"Sharma", "Walsh", "Chen", "Moreau", "Bianchi", "Nakamura", "Flores",
	"Bergstrom", "Abbas", "Delgado",
}

// emailDomains are all reserved, non-routable domains (RFC 2606 example.* and the
// reserved .test TLD), so no address can ever reach a real mailbox.
var emailDomains = []string{
	"example.com", "example.net", "example.org",
	"mail.test", "inbox.test", "sample.test", "demo.example",
}

var streetNames = []string{
	"Maple", "Oak", "Cedar", "Birch", "Elm", "Willow", "Pine", "Chestnut",
	"Sunset", "Lakeview", "Riverside", "Meadow", "Highland", "Aspen", "Juniper",
	"Magnolia", "Hawthorne", "Bayberry", "Sycamore", "Larkspur",
}

var streetSuffixes = []string{"St", "Ave", "Rd", "Ln", "Dr", "Blvd", "Way", "Ct", "Ter", "Pl"}

// areaCodes are real, geographic US area codes. Combined with the 555-01xx line
// range below (the block reserved for fictional use), the numbers look plausible
// but can never dial a real subscriber.
var areaCodes = []string{
	"212", "312", "415", "512", "617", "206", "305", "713", "404", "602",
	"503", "720", "615", "919", "858", "612", "614", "919", "480", "216",
}

type cityStateZip struct {
	City  string
	State string
	Zip   string
}

// locations are real, internally-consistent city/state/ZIP combinations. Only the
// street number and name (generated separately) are invented.
var locations = []cityStateZip{
	{"Austin", "TX", "78701"},
	{"Seattle", "WA", "98101"},
	{"Denver", "CO", "80202"},
	{"Portland", "OR", "97205"},
	{"Boston", "MA", "02116"},
	{"Chicago", "IL", "60614"},
	{"Atlanta", "GA", "30303"},
	{"Miami", "FL", "33130"},
	{"Nashville", "TN", "37203"},
	{"San Diego", "CA", "92101"},
	{"Minneapolis", "MN", "55401"},
	{"Columbus", "OH", "43215"},
	{"Raleigh", "NC", "27601"},
	{"Phoenix", "AZ", "85004"},
	{"Salt Lake City", "UT", "84101"},
	{"Kansas City", "MO", "64106"},
	{"Pittsburgh", "PA", "15222"},
	{"Sacramento", "CA", "95814"},
	{"Charlotte", "NC", "28202"},
	{"Madison", "WI", "53703"},
	{"Boise", "ID", "83702"},
	{"Richmond", "VA", "23219"},
	{"Providence", "RI", "02903"},
	{"Omaha", "NE", "68102"},
	{"Tampa", "FL", "33602"},
}

// Customers returns n synthetic customers generated deterministically from seed.
// Names are drawn from diverse invented pools, emails are unique and derived from
// the name plus a reserved domain, phone numbers use the fictional 555-01xx block,
// and addresses pair a real city/state/ZIP with an invented street. The zero-value
// CreatedAt is left for the database to populate on insert.
func Customers(n int, seed int64) []domain.Customer {
	if n <= 0 {
		return nil
	}
	r := newRand(seed)
	used := make(map[string]struct{}, n)
	out := make([]domain.Customer, 0, n)
	for i := 0; i < n; i++ {
		first := firstNames[r.Intn(len(firstNames))]
		last := lastNames[r.Intn(len(lastNames))]
		email := uniqueEmail(r, first, last, used)
		phone := makePhone(r)
		addr := makeAddress(r)
		out = append(out, domain.Customer{
			ID:      randUUID(r),
			Name:    first + " " + last,
			Email:   email,
			Phone:   phone,
			Address: addr,
		})
	}
	return out
}

// uniqueEmail builds a plausible, unique email from a name, varying the local-part
// style and domain. A numeric suffix is appended only if needed to break a clash.
func uniqueEmail(r *rand.Rand, first, last string, used map[string]struct{}) string {
	f := sanitize(first)
	l := sanitize(last)
	dom := emailDomains[r.Intn(len(emailDomains))]

	var local string
	switch r.Intn(5) {
	case 0:
		local = f + "." + l
	case 1:
		local = f + l
	case 2:
		local = f[:1] + "." + l
	case 3:
		local = f + "_" + l
	default:
		local = l + "." + f
	}

	email := local + "@" + dom
	for i := 1; ; i++ {
		if _, taken := used[email]; !taken {
			used[email] = struct{}{}
			return email
		}
		email = fmt.Sprintf("%s%d@%s", local, i, dom)
	}
}

// sanitize lowercases s and strips everything but ASCII letters and digits, so
// names like "OBrien" become clean email local-parts.
func sanitize(s string) string {
	var b strings.Builder
	for _, ch := range strings.ToLower(s) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// makePhone returns a US-format number in the 555-0100..555-0199 range reserved
// for fiction, so it is realistic-looking yet guaranteed non-dialable.
func makePhone(r *rand.Rand) string {
	ac := areaCodes[r.Intn(len(areaCodes))]
	return fmt.Sprintf("+1 (%s) 555-01%02d", ac, r.Intn(100))
}

// makeAddress pairs an invented street with a real city/state/ZIP.
func makeAddress(r *rand.Rand) domain.Address {
	num := 100 + r.Intn(9800)
	street := streetNames[r.Intn(len(streetNames))]
	suffix := streetSuffixes[r.Intn(len(streetSuffixes))]
	loc := locations[r.Intn(len(locations))]

	line2 := ""
	switch r.Intn(3) {
	case 0:
		line2 = fmt.Sprintf("Apt %d%c", 1+r.Intn(30), rune('A'+r.Intn(6)))
	case 1:
		line2 = fmt.Sprintf("Suite %d", 100+r.Intn(400))
	}

	return domain.Address{
		Line1:      fmt.Sprintf("%d %s %s", num, street, suffix),
		Line2:      line2,
		City:       loc.City,
		State:      loc.State,
		PostalCode: loc.Zip,
		Country:    "US",
	}
}
