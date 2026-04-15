package storage

// Modify wraps a future storage mutation such as Put/Delete.
type Modify struct {
	Data interface{}
}

type Put struct {
	Cf  string
	Key []byte
	Val []byte
}

type Delete struct {
	Cf  string
	Key []byte
}

func (m *Modify) Key() []byte {
	switch data := m.Data.(type) {
	case Put:
		return data.Key
	case Delete:
		return data.Key
	default:
		return nil
	}
}

func (m *Modify) Value() []byte {
	if data, ok := m.Data.(Put); ok {
		return data.Val
	}

	return nil
}

func (m *Modify) Cf() string {
	switch data := m.Data.(type) {
	case Put:
		return data.Cf
	case Delete:
		return data.Cf
	default:
		return ""
	}
}
