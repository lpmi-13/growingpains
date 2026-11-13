package catalog

// Category names and the nouns sold in each. There are 38, the number the plan's narration
// example quotes ("spread across 38 categories").
var categoryNouns = []struct {
	Slug  string
	Name  string
	Nouns []string
}{
	{"kitchen", "Kitchen", []string{"cutting board", "chef knife", "skillet", "saucepan", "spatula", "whisk", "colander", "mixing bowl"}},
	{"tableware", "Tableware", []string{"dinner plate", "side plate", "serving bowl", "mug", "teacup", "platter", "carafe", "napkin set"}},
	{"lighting", "Lighting", []string{"desk lamp", "pendant light", "floor lamp", "lantern", "wall sconce", "string lights", "candle holder"}},
	{"furniture", "Furniture", []string{"side table", "bookshelf", "stool", "bench", "armchair", "dresser", "coffee table", "nightstand"}},
	{"textiles", "Textiles", []string{"throw blanket", "cushion cover", "rug", "tablecloth", "tea towel", "curtain", "quilt"}},
	{"garden", "Garden", []string{"planter", "watering can", "trowel", "seed tray", "bird feeder", "garden kneeler", "pruning shears"}},
	{"bath", "Bath", []string{"bath towel", "soap dish", "bath mat", "shower caddy", "toothbrush holder", "bath brush"}},
	{"stationery", "Stationery", []string{"notebook", "fountain pen", "pencil case", "desk organiser", "letter set", "journal", "planner"}},
	{"jewellery", "Jewellery", []string{"necklace", "ring", "bracelet", "earrings", "brooch", "pendant", "anklet"}},
	{"bags", "Bags", []string{"tote bag", "backpack", "crossbody bag", "wallet", "card holder", "duffel bag", "pouch"}},
	{"womens-clothing", "Women's clothing", []string{"cardigan", "dress", "blouse", "skirt", "scarf", "jumper", "jacket"}},
	{"mens-clothing", "Men's clothing", []string{"shirt", "overshirt", "chinos", "knit hat", "flannel shirt", "waistcoat", "hoodie"}},
	{"kids", "Kids", []string{"wooden toy", "puzzle", "plush animal", "building blocks", "storybook", "play mat", "rattle"}},
	{"pets", "Pets", []string{"dog collar", "cat bed", "leash", "pet bowl", "chew toy", "scratching post", "pet blanket"}},
	{"art", "Art prints", []string{"art print", "poster", "linocut", "watercolour", "photograph", "screen print", "etching"}},
	{"ceramics", "Ceramics", []string{"vase", "bud vase", "pot", "jug", "bowl", "tumbler", "incense holder"}},
	{"woodwork", "Woodwork", []string{"spoon", "serving board", "coaster set", "box", "shelf", "spice rack", "key holder"}},
	{"candles", "Candles", []string{"pillar candle", "taper candle", "tin candle", "wax melt", "tealight set", "jar candle"}},
	{"skincare", "Skincare", []string{"face oil", "hand cream", "lip balm", "body butter", "soap bar", "facial mist"}},
	{"tea-coffee", "Tea and coffee", []string{"loose leaf tea", "coffee beans", "pour over", "tea caddy", "french press", "grinder"}},
	{"pantry", "Pantry", []string{"honey", "jam", "olive oil", "spice blend", "hot sauce", "chutney", "granola"}},
	{"music", "Music", []string{"ukulele", "guitar strap", "drum sticks", "harmonica", "kalimba", "music stand"}},
	{"books", "Books", []string{"cookbook", "field guide", "poetry book", "sketchbook", "photo book", "zine"}},
	{"games", "Games", []string{"board game", "card deck", "chess set", "dice set", "jigsaw", "backgammon set"}},
	{"outdoor", "Outdoor", []string{"camping mug", "hammock", "picnic blanket", "water bottle", "head torch", "camp stool"}},
	{"cycling", "Cycling", []string{"bike bell", "saddle bag", "bike light", "handlebar grips", "pannier", "cycling cap"}},
	{"fitness", "Fitness", []string{"yoga mat", "kettlebell", "skipping rope", "resistance band", "foam roller", "gym bag"}},
	{"tech", "Tech accessories", []string{"phone case", "laptop sleeve", "cable organiser", "phone stand", "charging dock"}},
	{"wall-decor", "Wall decor", []string{"wall hanging", "mirror", "clock", "picture frame", "macrame", "tapestry"}},
	{"storage", "Storage", []string{"basket", "crate", "storage jar", "drawer divider", "hook rack", "trunk"}},
	{"party", "Party", []string{"bunting", "party hat", "gift wrap", "greeting card", "cake topper", "piñata"}},
	{"wedding", "Wedding", []string{"guest book", "ring box", "veil", "place card", "cake stand", "garter"}},
	{"craft-supplies", "Craft supplies", []string{"yarn", "embroidery kit", "fabric bundle", "bead set", "paint set", "brush set"}},
	{"sewing", "Sewing", []string{"pin cushion", "sewing pattern", "thimble", "fabric scissors", "tape measure", "bias tape"}},
	{"shoes", "Shoes", []string{"slippers", "sandals", "boots", "trainers", "espadrilles", "clogs"}},
	{"accessories", "Accessories", []string{"sunglasses", "belt", "hair clip", "gloves", "umbrella", "tie"}},
	{"vintage", "Vintage", []string{"vintage lamp", "vintage chair", "vintage print", "vintage jug", "vintage clock"}},
	{"sale", "Sale", []string{"bundle", "gift set", "sample box", "mystery box", "seconds bundle"}},
}

var adjectives = []string{
	"rustic", "minimal", "handmade", "classic", "modern", "vintage", "bold", "soft", "sturdy",
	"elegant", "cosy", "bright", "natural", "hand-thrown", "speckled", "matte", "glossy", "woven",
	"carved", "recycled", "organic", "compact", "oversized", "everyday", "heirloom", "folk",
	"nordic", "coastal", "botanical", "geometric", "striped", "marbled", "hammered", "rough-hewn",
}

var materials = []string{
	"oak", "walnut", "ash", "cherry", "bamboo", "linen", "cotton", "wool", "leather", "brass",
	"copper", "steel", "stoneware", "porcelain", "glass", "cork", "rattan", "jute", "hemp",
	"silver", "beeswax", "soy wax", "clay", "marble", "slate", "felt", "silk", "canvas",
}

var colours = []string{
	"sage", "ochre", "charcoal", "ivory", "terracotta", "navy", "rust", "mustard", "forest",
	"blush", "slate", "sand", "teal", "plum", "olive", "cream", "indigo", "coral",
}

var sellerFirst = []string{
	"Hazel", "Wren", "Juniper", "Fern", "Moss", "Birch", "Willow", "Rowan", "Heather", "Ember",
	"Linden", "Maple", "Sorrel", "Bramble", "Thistle", "Clover", "Alder", "Aspen", "Cedar", "Lark",
}

var sellerSecond = []string{
	"Workshop", "Studio", "Supply Co.", "Makers", "Goods", "Atelier", "House", "Collective",
	"Trading", "Craft", "Mill", "Forge", "Loom", "Kiln", "Press", "Garden",
}

var sentences = []string{
	"Made by hand in small batches, so every %s is slightly different.",
	"This %s is finished in %s and built to last for years of everyday use.",
	"We source our %s from a family business that has worked with it for three generations.",
	"Pairs well with anything in %s or %s tones.",
	"Each piece is inspected twice before it leaves the studio.",
	"Wipe clean with a damp cloth; avoid harsh detergents to keep the %s looking its best.",
	"Ships in recycled packaging with no plastic.",
	"A thoughtful gift for anyone who loves %s things.",
	"The %s gives it a %s feel that softens with age.",
	"Dimensions vary slightly, as this is a handmade %s.",
	"Inspired by the %s homes we grew up in.",
	"Available in several sizes and finishes; see the options above.",
	"Our best-selling %s, now in a new %s colourway.",
	"Designed to be repaired rather than replaced.",
	"Every order supports apprentices learning the craft of working with %s.",
}

var reviewBodies = []string{
	"Lovely quality, arrived quickly.",
	"Exactly as described. Would buy again.",
	"Smaller than I expected but beautifully made.",
	"The colour is even nicer in person.",
	"Took a while to arrive, but worth the wait.",
	"Bought as a gift and it went down very well.",
	"Solid and well finished.",
	"Not quite what I hoped for, but the seller was helpful.",
	"Fantastic craftsmanship.",
	"Good value for money.",
}
