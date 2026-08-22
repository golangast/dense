package jim

type IntSeq struct {
	count int
}

func (i *IntSeq) Next() int {
	i.count++
	return i.count
}

type List[T any] struct {
	Value T
}

func MapValues[T any](value T) T {
	return value
}

type Greeter interface {
	Method()
}

type Person struct {
	Name string
	Age  int
}

func Greet() string {
	return "hello"
}

var Value = 42
var Scores = map[string]int{"demo": 1}
