package iridium

// USB identifiers taken from Iridium's signed Windows driver
// (irdmbc5vcom.inf, DriverVer 08/26/2016, 6.3.9600.9325).

// USBVendorID is the USB vendor ID assigned to Iridium handsets.
const USBVendorID uint16 = 0x1EDD

// Product is a USB product the official Iridium driver binds to.
type Product struct {
	// ProductID is the USB product ID.
	ProductID uint16
	// Model is the handset's model name, such as "Iridium 9575".
	Model string
}

// knownProducts are the products of the official driver's INF file.
var knownProducts = [...]Product{
	{ProductID: 0x9555, Model: "Iridium 9555"},
	{ProductID: 0xA555, Model: "Iridium 9555A"},
	{ProductID: 0x9575, Model: "Iridium 9575"},
	{ProductID: 0x5054, Model: "Iridium 9575P"},
	{ProductID: 0x7545, Model: "Iridium 9575E"},
	{ProductID: 0x520C, Model: "Iridium ROC (service port)"},
}

// KnownProducts returns the products the official Iridium driver binds to,
// in the driver's order. The slice is a fresh copy for the caller to keep.
func KnownProducts() []Product {
	products := knownProducts
	return products[:]
}

// ModelForProductID returns the model name for a USB product ID, and whether
// it is one the official driver knows.
func ModelForProductID(productID uint16) (string, bool) {
	for _, product := range knownProducts {
		if product.ProductID == productID {
			return product.Model, true
		}
	}
	return "", false
}
