package mockdata

import (
	"fmt"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// Category names used across the catalog.
const (
	catElectronics = "Electronics"
	catHome        = "Home & Kitchen"
	catSports      = "Sports & Outdoors"
	catBooks       = "Books"
	catApparel     = "Apparel"
	catBeauty      = "Beauty"
	catToys        = "Toys"
	catOffice      = "Office"
)

// categoryPrefix maps each category to its SKU prefix (e.g. "ELEC-0007").
var categoryPrefix = map[string]string{
	catElectronics: "ELEC",
	catHome:        "HOME",
	catSports:      "SPRT",
	catBooks:       "BOOK",
	catApparel:     "APRL",
	catBeauty:      "BEAU",
	catToys:        "TOYS",
	catOffice:      "OFFC",
}

// productSpec is a catalog entry before its SKU is assigned. Brand names are all
// invented for this project; none are real trademarks. Descriptions are written
// specifically for each item (no placeholder text). Prices are in minor units.
type productSpec struct {
	Name        string
	Description string
	Category    string
	Brand       string
	Price       int64
}

// catalog is a curated, hand-written set of 80 products across eight categories.
var catalog = []productSpec{
	// ---- Electronics ----
	{"Wireless Noise-Cancelling Headphones", "Over-ear Bluetooth headphones with active noise cancellation and a 30-hour battery.", catElectronics, "Aurio", 19999},
	{"True Wireless Earbuds", "Sweat-resistant earbuds with a pocket-sized charging case and touch controls.", catElectronics, "Voltcrest", 8999},
	{"4K Streaming Media Player", "Compact player that streams 4K HDR video to any HDMI television.", catElectronics, "Nimbus Labs", 4999},
	{"Portable Bluetooth Speaker", "Waterproof speaker with deep bass and up to 12 hours of playback.", catElectronics, "Pulsewave", 5999},
	{"27-inch QHD Monitor", "Height-adjustable 1440p display with a smooth 144Hz refresh rate.", catElectronics, "Kestrel", 27999},
	{"Mechanical Gaming Keyboard", "Backlit keyboard with hot-swappable tactile switches and a detachable cable.", catElectronics, "Voltcrest", 8999},
	{"Wireless Ergonomic Mouse", "Contoured mouse with silent clicks and adjustable DPI for all-day comfort.", catElectronics, "Aurio", 3499},
	{"USB-C Charging Hub", "Seven-port hub delivering 100W passthrough charging and 4K video out.", catElectronics, "Nimbus Labs", 5499},
	{"Smart Fitness Watch", "Water-resistant watch that tracks heart rate, sleep, and GPS routes.", catElectronics, "Pulsewave", 14999},
	{"1080p Webcam", "Auto-focusing webcam with a built-in dual microphone for crisp video calls.", catElectronics, "Kestrel", 6999},

	// ---- Home & Kitchen ----
	{"Stainless Steel French Press 34oz", "Double-walled press that keeps freshly brewed coffee hot for hours.", catHome, "Hearthstone", 3999},
	{"Ceramic Nonstick Fry Pan 10in", "PFOA-free pan with an even-heating aluminum core and a stay-cool handle.", catHome, "Copperleaf", 3499},
	{"Programmable Drip Coffee Maker", "Twelve-cup brewer with a reusable filter and a 24-hour timer.", catHome, "Marlow & Finch", 6499},
	{"Cast Iron Dutch Oven 6qt", "Enameled pot ideal for braising, roasting, and baking crusty bread.", catHome, "Hearthstone", 8999},
	{"Electric Kettle 1.7L", "Fast-boiling glass kettle with automatic shut-off and boil-dry protection.", catHome, "Verdapot", 4299},
	{"Bamboo Cutting Board Set", "Three reversible boards with juice grooves and easy-grip handles.", catHome, "Stonebrook", 2999},
	{"Stand Mixer 5.5qt", "Tilt-head mixer with ten speeds and a brushed stainless steel bowl.", catHome, "Copperleaf", 24999},
	{"Air Purifier for Medium Rooms", "HEPA purifier that clears smoke, dust, and pet dander from the air.", catHome, "Verdapot", 12999},
	{"Memory Foam Bath Mat", "Quick-drying mat with a plush surface and a non-slip backing.", catHome, "Stonebrook", 2499},
	{"Glass Food Storage Container Set", "Ten leak-proof containers with airtight snap-lock lids.", catHome, "Marlow & Finch", 3999},

	// ---- Sports & Outdoors ----
	{"Insulated Stainless Water Bottle 32oz", "Vacuum-sealed bottle that keeps drinks cold for a full 24 hours.", catSports, "Trailhead", 2999},
	{"Adjustable Dumbbell Pair", "Space-saving dumbbells that adjust from 5 to 52 pounds with a dial.", catSports, "Summitline", 32999},
	{"Non-Slip Yoga Mat 6mm", "Cushioned mat with alignment lines and an included carry strap.", catSports, "Ridgeback", 3999},
	{"Two-Person Backpacking Tent", "Lightweight three-season tent with a full rainfly and taped seams.", catSports, "Cascade Peak", 15999},
	{"Trekking Poles Pair", "Collapsible aluminum poles with cork grips and carbide trail tips.", catSports, "Northwind", 5499},
	{"Camp Sleeping Bag 20F", "Mummy bag rated for cold nights with a snag-free two-way zipper.", catSports, "Cascade Peak", 8999},
	{"Resistance Band Set", "Five stackable bands with handles, ankle straps, and a door anchor.", catSports, "Summitline", 3499},
	{"Hydration Daypack 12L", "Ventilated pack with a two-liter reservoir and quick-access gear loops.", catSports, "Trailhead", 6499},
	{"Foam Roller 18in", "High-density roller for muscle recovery and pre-workout mobility.", catSports, "Ridgeback", 2799},
	{"Rechargeable LED Headlamp", "Bright headlamp with a red night mode and a comfortable adjustable strap.", catSports, "Northwind", 2499},

	// ---- Books ----
	{"The Cartographer's Daughter", "A sweeping novel about a mapmaker who charts a coastline that keeps changing.", catBooks, "Lanternhouse Press", 1899},
	{"Salt and Ember", "A slow-burn fantasy about two rival alchemists bound by an old debt.", catBooks, "Quill & Anchor", 1699},
	{"Weeknight Kitchen", "A cookbook of 100 fast, seasonal dinners for busy households.", catBooks, "Meridian Books", 2999},
	{"The Quiet Algorithm", "A clear-eyed guide to thinking well in an age of noisy data.", catBooks, "Meridian Books", 2499},
	{"Letters from the Lighthouse", "A tender story of a keeper who writes to a stranger every winter.", catBooks, "Foxglove Press", 1599},
	{"Roots and Rituals", "An illustrated guide to starting and keeping a thriving home herb garden.", catBooks, "Lanternhouse Press", 2299},
	{"The Midnight Archive", "A mystery set in a library where the overdue books never came back.", catBooks, "Quill & Anchor", 1799},
	{"Small Brave Things", "A warm collection of essays on finding courage in ordinary days.", catBooks, "Foxglove Press", 1499},
	{"Tides of the Northern Sea", "A historical saga following a fishing family across three generations.", catBooks, "Meridian Books", 2099},
	{"The Beginner's Telescope", "A friendly introduction to stargazing without expensive equipment.", catBooks, "Lanternhouse Press", 1999},

	// ---- Apparel ----
	{"Merino Wool Crew Socks 3-Pack", "Breathable wool socks that regulate temperature in any season.", catApparel, "Fielder & Co", 2499},
	{"Organic Cotton Crewneck Tee", "Soft, pre-shrunk everyday shirt cut for a clean tailored fit.", catApparel, "Loomcraft", 1999},
	{"Packable Rain Jacket", "Lightweight waterproof shell that folds neatly into its own pocket.", catApparel, "Harbor Knit", 6999},
	{"Fleece-Lined Beanie", "Warm ribbed hat with a snug, itch-free brushed lining.", catApparel, "Cedar & Pine", 1799},
	{"Slim-Fit Chino Pants", "Stretch-cotton chinos that move easily from the office to the weekend.", catApparel, "Everstitch", 4999},
	{"Quarter-Zip Pullover", "Midweight pullover with a stand collar and thumbhole cuffs.", catApparel, "Harbor Knit", 5499},
	{"Canvas Low-Top Sneakers", "Classic lace-up sneakers with a cushioned removable insole.", catApparel, "Fielder & Co", 4499},
	{"Insulated Puffer Vest", "Packable vest with recycled-fill insulation and zippered hand pockets.", catApparel, "Cedar & Pine", 7999},
	{"Full-Grain Leather Belt", "Handsome everyday belt finished with a brushed-nickel buckle.", catApparel, "Everstitch", 3499},
	{"Performance Ankle Socks 6-Pack", "Cushioned athletic socks with targeted arch support and mesh venting.", catApparel, "Loomcraft", 1999},

	// ---- Beauty ----
	{"Vitamin C Brightening Serum", "Lightweight serum that evens skin tone and adds a natural glow.", catBeauty, "Botanica Bloom", 2899},
	{"Hyaluronic Acid Moisturizer", "Oil-free gel cream that hydrates deeply without a greasy finish.", catBeauty, "Luminel", 2499},
	{"Gentle Foaming Cleanser", "Sulfate-free wash that removes makeup without stripping the skin.", catBeauty, "Botanica Bloom", 1699},
	{"Mineral Sunscreen SPF 50", "Non-greasy zinc sunscreen light enough for daily wear under makeup.", catBeauty, "Luminel", 1999},
	{"Argan Oil Hair Mask", "Weekly deep-conditioning treatment for dry, frizz-prone hair.", catBeauty, "Petal & Poppy", 1899},
	{"Bamboo Charcoal Face Mask", "Clay mask that draws out impurities and visibly refines pores.", catBeauty, "Verde Skincare", 1599},
	{"Nourishing Lip Balm Trio", "Three tinted balms made with shea butter and beeswax.", catBeauty, "Petal & Poppy", 1299},
	{"Rosewater Facial Toner", "Alcohol-free mist that soothes skin and preps it for moisturizer.", catBeauty, "Botanica Bloom", 1499},
	{"Retinol Night Cream", "Time-release cream that smooths the look of fine lines overnight.", catBeauty, "Verde Skincare", 3299},
	{"Soft-Bristle Cleansing Brush", "Silicone facial brush for a deeper yet gentler daily cleanse.", catBeauty, "Luminel", 3999},

	// ---- Toys ----
	{"Wooden Building Blocks 100-Piece", "Smooth hardwood blocks finished in bright, non-toxic colors.", catToys, "Tinkertop", 3499},
	{"STEM Robotics Starter Kit", "Buildable robot that teaches the basics of coding through play.", catToys, "Bramblewood", 5999},
	{"Weighted Plush Sloth 16in", "Huggable, gently weighted plush with soft embroidered eyes.", catToys, "Playnest", 2499},
	{"500-Piece Jigsaw Puzzle", "Scenic landscape puzzle with a poster-size finished image.", catToys, "Bramblewood", 1699},
	{"Magnetic Tile Set 60-Piece", "Translucent tiles that click together into towers and shapes.", catToys, "Tinkertop", 4499},
	{"Kids Watercolor Paint Set", "Washable paints with brushes and a spill-resistant mixing tray.", catToys, "Jumbo Jibs", 1999},
	{"All-Terrain Remote Control Car", "Grippy off-road car with a rechargeable battery and 2.4GHz control.", catToys, "Bramblewood", 3999},
	{"Wooden Train Track Expansion", "Twenty-six pieces compatible with most standard wooden railways.", catToys, "Tinkertop", 2899},
	{"Play Kitchen Accessory Set", "Twenty-five felt food pieces for imaginative pretend cooking.", catToys, "Playnest", 2299},
	{"Glow-in-the-Dark Star Stickers", "Reusable ceiling stars that transform a bedroom into a night sky.", catToys, "Jumbo Jibs", 999},

	// ---- Office ----
	{"Gel Ink Pens 12-Pack", "Quick-drying pens with a smooth 0.5mm point in assorted colors.", catOffice, "Penmark", 1299},
	{"Hardcover Dotted Notebook A5", "192-page notebook with lay-flat binding and a ribbon bookmark.", catOffice, "Foliofield", 1899},
	{"Adjustable Laptop Stand", "Aluminum stand that raises the screen to a comfortable eye level.", catOffice, "Deskwright", 3999},
	{"Rotating Desk Organizer Caddy", "Spinning caddy with slots for pens, sticky notes, and paper clips.", catOffice, "Clarity Co", 2499},
	{"Sticky Notes Rainbow Pack", "Twelve pads of repositionable notes in twelve bright colors.", catOffice, "Penmark", 999},
	{"Wireless Desk Charger Pad", "Slim pad that fast-charges phones and earbuds without a cable.", catOffice, "Deskwright", 2999},
	{"Ergonomic Footrest", "Angled, textured footrest that reduces leg fatigue at the desk.", catOffice, "Clarity Co", 3499},
	{"Leather Desk Pad", "Water-resistant mat that protects the desk and smooths handwriting.", catOffice, "Foliofield", 4499},
	{"Heavy-Duty Stapler", "All-metal stapler that punches cleanly through up to 40 sheets.", catOffice, "Penmark", 1599},
	{"Cable Management Kit", "Clips, sleeves, and ties to tidy a cluttered workspace in minutes.", catOffice, "Deskwright", 1799},
}

// Products returns the full curated catalog with deterministic, unique SKUs
// assigned per category (e.g. "ELEC-0001"). The result is identical on every call.
// The zero-value CreatedAt is left for the database to populate on insert.
func Products() []domain.Product {
	counters := make(map[string]int, len(categoryPrefix))
	out := make([]domain.Product, 0, len(catalog))
	for _, s := range catalog {
		prefix := categoryPrefix[s.Category]
		counters[prefix]++
		out = append(out, domain.Product{
			SKU:         fmt.Sprintf("%s-%04d", prefix, counters[prefix]),
			Name:        s.Name,
			Description: s.Description,
			Category:    s.Category,
			Brand:       s.Brand,
			Price:       s.Price,
			Currency:    "USD",
		})
	}
	return out
}
