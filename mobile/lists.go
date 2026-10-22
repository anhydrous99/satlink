package mobile

// Lists for the host languages: gomobile binds no slices but []byte, so
// lists are types with Len and Get. Get returns the zero value — "" or
// nil — for an index outside 0 to Len()-1, as a panic would end the app.

// StringList is a list of strings.
type StringList struct {
	items []string
}

// Len returns the number of strings.
func (l *StringList) Len() int {
	return len(l.items)
}

// Get returns the string at index, or "" when index is out of range.
func (l *StringList) Get(index int) string {
	if index < 0 || index >= len(l.items) {
		return ""
	}
	return l.items[index]
}

// SMSMessageList is a list of messages.
type SMSMessageList struct {
	items []SMSMessage
}

// Len returns the number of messages.
func (l *SMSMessageList) Len() int {
	return len(l.items)
}

// Get returns a copy of the message at index, or nil when index is out of
// range.
func (l *SMSMessageList) Get(index int) *SMSMessage {
	if index < 0 || index >= len(l.items) {
		return nil
	}
	message := l.items[index]
	return &message
}

// KnownProductList is a list of USB products.
type KnownProductList struct {
	items []KnownProduct
}

// Len returns the number of products.
func (l *KnownProductList) Len() int {
	return len(l.items)
}

// Get returns a copy of the product at index, or nil when index is out of
// range.
func (l *KnownProductList) Get(index int) *KnownProduct {
	if index < 0 || index >= len(l.items) {
		return nil
	}
	product := l.items[index]
	return &product
}
