# 面向 JavaScript / TypeScript 开发者的 Go 入门

本文以 **Go 1.27.3** 为基准，面向已经使用 JavaScript、TypeScript、React 或 Node.js 开发产品的读者。内容从语言基础推进到资源管理、并发和代码组织，目标是能够阅读、修改真实的 Go 工程。示例只使用标准库，与具体产品和界面框架无关。

Go 的语法不多，但它对值、接口、错误和并发的处理与 JavaScript 有明显差异。理解这些差异，比把熟悉的写法逐句翻译成 Go 更有用。

本文标为“完整程序”的 Go 代码块都可以单独保存为 `main.go` 后执行；不同代码块不需要拼在一起。“完整测试文件”需要保存为 `words_test.go`。代码开头的 `tutorial` 注释仅用于自动验证，不影响运行。在本仓库根目录执行 `go run ./scripts/check-tutorial.go`，会在临时目录编译、运行并检查所有 Go 示例，不需要下载依赖。

## 目录

1. [程序、包与工具链](#1-程序包与工具链)
2. [变量、类型与控制流](#2-变量类型与控制流)
3. [值、指针与共享数据](#3-值指针与共享数据)
4. [字符串与集合](#4-字符串与集合)
5. [函数、闭包与资源生命周期](#5-函数闭包与资源生命周期)
6. [结构体、方法与组合](#6-结构体方法与组合)
7. [接口与错误处理](#7-接口与错误处理)
8. [泛型与现代集合写法](#8-泛型与现代集合写法)
9. [I/O、JSON 与标准库组合](#9-iojson-与标准库组合)
10. [并发与同步](#10-并发与同步)
11. [取消、后台任务与接口契约](#11-取消后台任务与接口契约)
12. [阅读与验证真实仓库](#12-阅读与验证真实仓库)

## 1. 程序、包与工具链

### 一个程序从哪里开始

完整程序：

```go
// tutorial: hello
package main

import "fmt"

func main() {
	fmt.Println("你好，Go")
}
```

`package main` 表示这是一个可执行程序的包；`func main()` 是入口。`fmt` 是标准库包，`fmt.Println` 是它导出的函数。Go 用标识符首字母的大小写控制包边界：`Println` 可以被其他包使用，`println` 不能作为这个包的公开 API。

在一个新的练习目录中运行：

```sh
go mod init example.com/hello
go run .
go build -o hello .
./hello
```

`go run` 先编译再运行；Go 不需要一个常驻的 JavaScript 解释器执行源码。默认的 Go 代码通常编译为本机机器码，程序仍然包含负责调度、垃圾回收等工作的 Go runtime。

### module 和 package 各自负责什么

一个 module 是一组一起版本化的包，由根目录的 `go.mod` 定义。一个目录通常对应一个包，该目录下参与当前构建的 Go 文件共享包级声明。包的文件不能像 ES module 那样各自形成独立作用域；导入声明则属于各自的文件，不能在一个文件导入、另一个文件直接使用该导入名。

一个最小的 `go.mod` 是：

```text
module example.com/hello

go 1.27.3
```

`module` 是导入路径的前缀，不要求练习项目必须已经发布到这个域名。`go` 行声明最低 Go 版本要求，并影响语言语义；它不是一个 npm 风格的依赖范围。工具链可能按配置自动选择或下载满足要求的版本。希望只使用本机版本时，可设置 `GOTOOLCHAIN=local`。

| 文件或目录 | 作用 |
| --- | --- |
| `go.mod` | module 路径、最低 Go 版本和模块依赖 |
| `go.sum` | 下载过的模块内容及其 `go.mod` 的校验值；不是 `package-lock.json` 的直接对应物 |
| `internal/` | 限制导入范围；只有其父目录树以内的代码可以导入其中的包 |
| `cmd/<程序名>/` | 常见的可执行程序入口组织方式，并非编译器强制要求 |
| `*_test.go` | 测试文件，普通程序构建不会包含它们 |

Go 根据模块图和最小版本选择规则决定依赖版本。添加依赖通常用 `go get 模块路径@具体版本`，整理实际需要的依赖用 `go mod tidy`。不要通过手工删除 `go.sum` 来“修复”不理解的依赖问题。

### 日常工具

```sh
go version
go fmt ./...
go vet ./...
go test ./...
go doc io.Reader
go list ./...
go mod tidy
```

`./...` 表示当前目录及子目录中的包。`go fmt` 统一格式，`go vet` 找常见可疑写法，`go test` 编译并执行测试；三者解决不同问题。未使用的导入和局部变量通常会直接导致编译失败。

## 2. 变量、类型与控制流

### 零值和显式类型

Go 变量始终有确定的类型。声明但未赋值的变量有零值：数字为 `0`，布尔为 `false`，字符串为 `""`，指针、slice、map、channel、函数和接口的零值为 `nil`。结构体的每个字段分别取零值。

`var count int` 声明变量；函数内部的 `count := 3` 同时声明和推导类型。`:=` 不是赋值运算符的通用替代：同一作用域内至少有一个非空白标识符必须是新变量。给已经存在的变量赋值用 `=`。

`const` 声明编译时常量，不是 JavaScript 那种“变量绑定不能重新赋值”。Go 没有用 `const` 声明不可变 slice、map 或结构体对象的机制。

完整程序：

```go
// tutorial: control
package main

import (
	"fmt"
	"strconv"
)

func parseLimit(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse limit %q: %w", s, err)
	}
	if n < 1 {
		return 0, fmt.Errorf("limit must be positive: %d", n)
	}
	return n, nil
}

func main() {
	limit, err := parseLimit("3")
	if err != nil {
		fmt.Println(err)
		return
	}

	total := 0
	for i := range limit {
		total += i
	}

	switch {
	case total > 10:
		fmt.Println("large")
	case total > 0:
		fmt.Println("positive:", total)
	default:
		fmt.Println("zero")
	}
}
```

输出 `positive: 3`。`range limit` 在这里依次得到 `0`、`1`、`2`。Go 还支持传统的 `for i := 0; i < limit; i++`；`for 条件` 相当于其他语言的 `while`；`for {}` 是无限循环。

Go 条件表达式必须是 `bool`，没有 truthy/falsy；不能直接写 `if count`。`switch` 的分支默认结束，不会像 JavaScript 那样自动贯穿到下一分支。

函数可以返回多个值，`(int, error)` 不需要先装进 tuple 或对象。调用方接收的是两个返回值；`_` 可以丢弃不需要的值，但不要用它习惯性忽略错误。

### 类型转换不会偷偷发生

`int` 和 `int64` 是不同类型，计算前通常需要显式转换，例如 `int64(count)`。转换不等于安全校验：整数缩窄转换可能截断；解析外部数据时仍然要检查范围。

`type UserID string` 定义一个新类型，编译器会区分它与 `string`；`type UserID = string` 则是别名，二者是同一类型。前者适合建立有意义的领域边界，后者常用于迁移或重新导出类型。

注意较小作用域中的遮蔽，例如 `if n, err := parseLimit(s); err != nil { ... }` 中的 `n`、`err` 仅属于这个 `if`。与外层同名时，它们可能是不同变量。不要为了少写一行声明，让真正需要更新的外层变量一直保持旧值。

## 3. 值、指针与共享数据

### Go 的赋值和传参都会复制值

在 JavaScript 中，两个变量很容易指向同一个对象。Go 的结构体赋值会复制结构体的值。要修改同一个结构体，可以显式传递指针。

完整程序：

```go
// tutorial: values
package main

import "fmt"

type Profile struct {
	Name string
	Tags []string
}

func rename(p *Profile, name string) {
	p.Name = name
}

func main() {
	a := Profile{Name: "Ada", Tags: []string{"reader"}}
	b := a
	b.Name = "Grace"
	b.Tags[0] = "writer"
	fmt.Println(a.Name, b.Name, a.Tags[0])

	rename(&a, "Lin")
	fmt.Println(a.Name)

	var missing *Profile
	fmt.Println(missing == nil)
}
```

输出：

```text
Ada Grace writer
Lin
true
```

`b.Name` 是复制后独立的字段，但 `Tags` 是 slice：复制 slice 值没有复制它引用的底层数组，所以 `b.Tags[0]` 改变了 `a` 看到的元素。这不是“结构体有时按引用传递”，而是结构体里面的值可能包含指向共享数据的引用。

`&a` 取得地址，`*Profile` 表示指向 `Profile` 的指针，`*p` 表示解引用。访问字段时 `p.Name` 会自动处理指针，不需要每次写 `(*p).Name`。解引用 nil 指针会 panic。

指针本身仍按值传递；被复制的是地址。函数通过地址修改指向的数据，不等于函数能自动替换调用方的指针变量。

### 不需要手动管理栈和堆

返回局部变量的地址在 Go 中是合法的；编译器会根据逃逸分析安排存储，垃圾回收器维护 Go 对象的生命周期。不要为了“避免栈变量失效”人为引入全局变量。

也不要把“使用指针”当作默认性能优化。指针会增加别名和共享状态；小结构体按值传递往往更清楚。真正的性能选择应来自测量。

“深拷贝”并不是结构体赋值、`slices.Clone` 或 `maps.Clone` 的统一保证。它们的复制深度取决于元素类型；如果元素里还有指针、slice 或 map，仍然可能共享数据。

## 4. 字符串与集合

### 字符串不是字符数组

Go 的 `string` 是不可变的字节序列，通常存储 UTF-8，但类型本身不保证内容一定是有效 UTF-8。`len(s)` 返回字节数；`s[i]` 返回一个 `byte`；`range s` 按 UTF-8 解码出 `rune` 和对应的字节偏移。

`byte` 是 `uint8` 的别名，`rune` 是 `int32` 的别名。一个 rune 是 Unicode 码点，不一定是用户眼中的一个完整字符：组合字符和 emoji 序列可能含多个码点。

### slice 的长度、容量和底层数组

数组 `[3]int` 的长度属于类型。slice `[]int` 是底层数组的一段视图，包含访问底层存储所需的信息、长度和容量。它是最常用的顺序集合。

完整程序：

```go
// tutorial: collections
package main

import (
	"fmt"
	"slices"
)

func main() {
	s := "Go语言"
	fmt.Println(len(s), len([]rune(s)))
	for offset, r := range s {
		fmt.Printf("%d:%c ", offset, r)
	}
	fmt.Println()

	items := []int{10, 20, 30}
	view := items[:2]
	view[0] = 99
	independent := slices.Clone(view)
	independent[0] = 7
	fmt.Println(items, independent)

	var names []string
	names = append(names, "Ada")
	fmt.Println(names, len(names))

	counts := make(map[string]int)
	counts["go"]++
	count, exists := counts["rust"]
	fmt.Println(count, exists)
	delete(counts, "go")
}
```

这里 `len("Go语言")` 是 `8`，rune 数量是 `4`，字节偏移依次为 `0`、`1`、`2`、`5`。

`append` 可能复用底层数组，也可能分配新数组，所以必须接收它返回的 slice：`items = append(items, value)`。原来的其他 slice 是否还能观察到新增元素或后续修改，取决于它们的长度、容量和是否共享数组。不要依赖一次 append “碰巧”分配来隔离状态。

`make([]T, n)` 创建长度为 `n` 的 slice，元素已经存在；`make([]T, 0, n)` 创建长度为零、预留容量为 `n` 的 slice。把前者再 append `n` 次，会得到 `2n` 个元素。

### nil 与空集合

nil slice 可以 `len`、`range` 和 `append`，通常是有用的零值。nil map 可以读取、求长度、遍历和 `delete`，但不能写入；写入前要用 `make` 或字面量初始化。

nil slice 与非 nil 的空 slice 在一些序列化协议中不同，例如传统 `encoding/json` 默认分别编码为 `null` 和 `[]`。这是接口契约问题，不能仅凭“长度都为零”忽略。

map 的遍历顺序没有保证；需要稳定输出时，显式排序键。map 不支持无同步的并发读写。slice 与 map 也不能直接用 `==` 比较内容，只能与 `nil` 比较；可以按需要使用 `slices.Equal` 或 `maps.Equal`，同时注意这些函数的元素类型约束。

## 5. 函数、闭包与资源生命周期

### 函数也是值

函数类型写作 `func(string) bool`，参数名不属于函数类型。把函数作为参数，常常足以实现一个局部扩展点；不必为每个回调引入只有一个实现的庞大接口。

完整程序：

```go
// tutorial: functions
package main

import (
	"fmt"
	"strings"
)

func filter(values []string, keep func(string) bool) []string {
	var result []string
	for _, value := range values {
		if keep(value) {
			result = append(result, value)
		}
	}
	return result
}

func main() {
	prefix := "go"
	keep := func(s string) bool {
		return strings.HasPrefix(s, prefix)
	}
	fmt.Println(filter([]string{"go", "rust", "gopher"}, keep))

	n := 1
	defer fmt.Println("argument:", n)
	defer func() {
		fmt.Println("closure:", n)
	}()
	n = 2
}
```

输出：

```text
[go gopher]
closure: 2
argument: 1
```

闭包捕获的是可访问的变量，不是自动冻结的快照。`defer fmt.Println(..., n)` 的参数在执行 defer 语句时就求值；第二个 defer 的函数体到返回前才运行，因此它读到后来的 `n`。

### defer 让清理靠近资源获取

`defer` 注册一个调用，在当前函数返回前执行；多个 defer 按后进先出顺序执行。常见用途是 `defer file.Close()`、`defer mu.Unlock()` 和 `defer cancel()`。

defer 属于函数，不属于最近的 `if`、`for` 或花括号块。在很长的循环中反复打开文件并 defer 关闭，会让所有文件一直保持打开，直到整个函数结束。可以把每次处理提取为独立函数，让资源在这一轮结束时释放。

资源获取失败时应先返回，获取成功后再注册清理。对写文件操作，`Close` 也可能报告写入失败；不能因为已经 `defer Close()` 就永远忽略其结果。读取文件时的关闭错误通常不改变已经获得的数据，但调用方仍需根据资源契约判断。

可变参数 `func logValues(values ...string)` 在函数内部表现为 `[]string`；调用时可以传多个字符串，也可以用 `logValues(existing...)` 展开现有 slice。

Go 1.22 起，在循环声明部分用 `:=` 定义的迭代变量采用每轮独立变量的语义，常见的旧版闭包捕获陷阱已有变化。但这不消除共享对象的数据竞争；如果循环用 `=` 复用外层变量，或闭包访问同一 slice/map/指针，仍需检查共享关系。

## 6. 结构体、方法与组合

### 方法属于类型

Go 没有 class 关键字。结构体存储数据，方法为类型提供行为，构造函数通常是普通的 `NewX` 函数。零值有用时，甚至不需要构造函数。

完整程序：

```go
// tutorial: methods
package main

import (
	"fmt"
	"strings"
)

type Counter struct {
	n int
}

func (c *Counter) Add(delta int) {
	c.n += delta
}

func (c Counter) Value() int {
	return c.n
}

type Report struct {
	Counter
	Title string
}

type Label struct {
	text string
}

func NewLabel(text string) Label {
	return Label{text: text}
}

func (l Label) Upper() Label {
	l.text = strings.ToUpper(l.text)
	return l
}

func (l Label) Text() string {
	return l.text
}

func main() {
	r := Report{Title: "Visits"}
	r.Add(2)
	fmt.Println(r.Title, r.Value())

	label := NewLabel("hello")
	fmt.Println(label.Upper().Text(), label.Text())
}
```

输出 `Visits 2` 和 `HELLO hello`。`Counter` 的零值可以直接使用。`Add` 用指针接收者修改原对象；`Value` 的值接收者获得一个副本。`Label.Upper` 返回改过的副本，所以方法链没有修改原来的 `label`。

看到链式 API 时，应检查每个方法的接收者和返回值，判断它是返回新值、修改同一对象，还是返回暂时有效的 builder。不要仅凭外形把它理解为 React 状态或不可变数据操作。

### method set 比“调用能否自动取址”更严格

类型 `T` 的方法集包含接收者为 `T` 的方法；`*T` 的方法集包含接收者为 `T` 和 `*T` 的方法。对可寻址变量 `c`，编译器允许把 `c.Add(1)` 简写为 `(&c).Add(1)`，但这不意味着 `T` 自动实现所有要求指针接收者方法的接口。

带有锁、拥有资源或明显需要保持同一身份的类型，通常使用指针接收者，并避免在使用后复制它们。一个类型的接收者风格应保持一致；本例混用两种接收者是为了展示语义，实际设计还要考虑整个类型的用途。

### 嵌入不是继承

`Report` 中的匿名字段 `Counter` 是嵌入。它仍然是一个字段，同时让部分字段和方法可以通过外层类型直接访问，所以 `r.Add` 可以转发到嵌入的 `Counter`。

这里没有 class 继承树，没有子类覆盖父类虚方法。嵌入内部的函数不会因为外层恰好有同名方法就动态派发到外层。方法名冲突也可能让提升失效。只是想持有一个依赖时，命名字段通常更明确。

## 7. 接口与错误处理

### 接口由使用方需要的行为组成

接口定义方法集合，类型只要满足方法集就自动实现，不需要 `implements` 声明。它有一点像 TypeScript 的结构化类型，但 Go 接口主要描述方法，不是任意对象字段的 shape。

`any` 是 `interface{}` 的别名，可以容纳任意 Go 值；它与 TypeScript 的 `any` 不同，不允许绕过静态检查随便访问属性。需要具体操作时，使用类型断言或 type switch。

接口适合表达真正存在的替换边界，例如输入源、时钟或存储。函数需要读取字节时接受 `io.Reader`，不必要求调用方传某一种具体文件对象；反过来，也不必为每个具体结构体立即建立同名接口。

### 接口里的 nil

完整程序：

```go
// tutorial: interfaces
package main

import "fmt"

type Namer interface {
	Name() string
}

type User struct {
	DisplayName string
}

func (u *User) Name() string {
	if u == nil {
		return "anonymous"
	}
	return u.DisplayName
}

func main() {
	var pointer *User
	var named Namer = pointer
	fmt.Println(pointer == nil, named == nil, named.Name())

	if user, ok := named.(*User); ok {
		fmt.Println(user == nil)
	}

	var value any = 42
	switch v := value.(type) {
	case int:
		fmt.Println(v + 1)
	case string:
		fmt.Println(len(v))
	default:
		fmt.Println("unsupported")
	}
}
```

第一行输出 `true false anonymous`。可以把接口值理解为“动态类型与动态值”这一对信息：`named` 的动态类型是 `*User`，动态值是 nil，因此整个接口不等于 nil。只有没有动态类型也没有动态值的接口才等于 nil。

这也影响 `error`：若一个函数返回 `error`，不要在成功时返回一个类型为 `*SomeError` 的 nil 指针，应直接 `return nil`。`value.(T)` 在类型不匹配时会 panic；`value.(T)` 配合两个接收值，即 `v, ok := value.(T)`，则允许检查失败。

### 错误也是值

`error` 接口只要求 `Error() string`。多数可以预期的失败通过返回值传递，调用方决定重试、显示提示或向上返回。错误文字供人阅读，程序分支应使用可识别的错误值或类型。

完整程序：

```go
// tutorial: errors
package main

import (
	"errors"
	"fmt"
	"strconv"
)

var ErrInvalidPort = errors.New("invalid port")

type InputError struct {
	Value string
	Err   error
}

func (e *InputError) Error() string {
	return fmt.Sprintf("input %q: %v", e.Value, e.Err)
}

func (e *InputError) Unwrap() error {
	return e.Err
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, &InputError{Value: s, Err: err}
	}
	if n < 1 || n > 65535 {
		return 0, &InputError{Value: s, Err: ErrInvalidPort}
	}
	return n, nil
}

func main() {
	_, err := parsePort("70000")
	if err == nil {
		return
	}
	err = fmt.Errorf("read settings: %w", err)
	fmt.Println(errors.Is(err, ErrInvalidPort))

	var input *InputError
	if errors.As(err, &input) {
		fmt.Println(input.Value)
	}
}
```

输出 `true` 和 `70000`。`%w` 包装一个错误，使调用方仍能沿错误链识别原因；`%v` 只格式化文本，不建立这种关系。`errors.Is` 检查某个错误是否匹配错误链中的原因；`errors.As` 找到指定类型并赋给目标变量。`&input` 是用来让 `As` 更新这个指针变量，不是随手多加一个 `&`。

多个独立失败需要一起返回时可以用 `errors.Join`，它保留 `Is`/`As` 可遍历的原因关系。是否保留底层错误是 API 设计的一部分：包装它意味着调用方可能依赖它。

在每一层补充必要上下文，然后在负责用户呈现的一层记录错误。每层都打印再向上返回，会产生重复日志。库函数也不应因为普通业务失败直接 `log.Fatal` 或 `os.Exit`；它们会结束整个进程，且 `os.Exit` 不运行 defer。

`panic` 用于无法继续的程序错误或特定 API 约定，不是日常业务异常。`recover` 只有在同一 goroutine 的延迟调用中才能拦截正在展开的 panic；它不会替你恢复被修改了一半的业务状态，也抓不到另一个 goroutine 的 panic。

## 8. 泛型与现代集合写法

### 类型参数让类型关系明确

完整程序：

```go
// tutorial: generics
package main

import (
	"cmp"
	"fmt"
	"iter"
	"maps"
	"slices"
)

type Stack[T any] struct {
	items []T
}

func (s *Stack[T]) Push(value T) {
	s.items = append(s.items, value)
}

func (s *Stack[T]) Pop() (T, bool) {
	if len(s.items) == 0 {
		var zero T
		return zero, false
	}
	i := len(s.items) - 1
	value := s.items[i]
	var zero T
	s.items[i] = zero
	s.items = s.items[:i]
	return value, true
}

func ascending[T cmp.Ordered](values []T) []T {
	result := slices.Clone(values)
	slices.Sort(result)
	return result
}

func countdown(n int) iter.Seq[int] {
	return func(yield func(int) bool) {
		for ; n > 0; n-- {
			if !yield(n) {
				return
			}
		}
	}
}

func main() {
	var stack Stack[string]
	stack.Push("Go")
	fmt.Println(stack.Pop())
	fmt.Println(ascending([]int{3, 1, 2}))

	counts := map[string]int{"b": 2, "a": 1}
	fmt.Println(slices.Sorted(maps.Keys(counts)))
	for n := range countdown(3) {
		fmt.Print(n, " ")
		if n == 2 {
			break
		}
	}
	fmt.Println()
}
```

`Stack[T]` 是参数化类型，`Stack[string]` 是具体类型。`T any` 表示可以使用任意类型，但函数只能执行对所有允许类型都合法的操作。上例清空弹出位置，是为了避免底层数组继续保留对已移除对象的引用；对 `string`、指针等类型尤其值得理解。

约束 `comparable` 允许比较键类型；`cmp.Ordered` 允许使用有序基本类型。自定义约束中的 `~int` 表示“底层类型是 int 的类型”，因此既包含 `int`，也包含 `type Count int`；`~int | ~int64` 表示类型集合的并集。这类含类型集合的接口通常用于约束，不能当普通值接口使用。

泛型方法可以使用接收者类型的参数，如 `Stack[T].Pop`；普通方法不能再声明自己独立的一组方法类型参数。需要这种能力时，通常用泛型函数表达。

### 迭代器不是异步流

`iter.Seq[T]` 的核心形状是 `func(yield func(T) bool)`。`range` 调用迭代函数；迭代函数调用 `yield` 交出值。消费者退出循环后，`yield` 返回 false，生产方必须停止。这里没有自动启动 goroutine，也没有自动创建 channel。

标准库的 `maps.Keys` 返回迭代器，`slices.Sorted` 收集并排序它。只有需要一次性物化所有结果时才收集为 slice。一个迭代器是否可以重复使用由它自己的契约决定；本例 `countdown` 捕获并修改 `n`，因此应当按一次性迭代器使用。

接口和泛型解决的问题不同。接口表达不同类型共有的行为，适合运行时替换；泛型保留输入输出之间的静态类型关系。不要因为 TS 常用泛型，就把每个 Go 函数都参数化。

## 9. I/O、JSON 与标准库组合

### 围绕能力组合数据流

`io.Reader` 表达读取字节，`io.Writer` 表达写入字节。文件、HTTP 响应体和内存缓冲区可以实现相同接口，因此一个处理函数不必关心数据来自哪种设备。

Reader 的 `Read` 可能同时返回 `n > 0` 和非 nil 错误；手写循环时要先处理已经读取的数据，再判断错误。多数场景可以使用 `io.Copy`、`io.ReadAll`、`bufio` 或编码器来处理这些细节。对外部输入，`ReadAll` 之前仍然要考虑最大尺寸。

完整程序：

```go
// tutorial: json-stream
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Settings struct {
	Name    string `json:"name"`
	Retries int    `json:"retries"`
}

func decodeSettings(r io.Reader) (Settings, error) {
	var settings Settings
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Settings{}, errors.New("settings contain more than one JSON value")
		}
		return Settings{}, fmt.Errorf("read end of settings: %w", err)
	}
	if settings.Retries < 0 {
		return Settings{}, errors.New("retries must not be negative")
	}
	return settings, nil
}

func main() {
	r := strings.NewReader(`{"name":"daily","retries":2}`)
	settings, err := decodeSettings(r)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(settings.Name, settings.Retries)
}
```

`json` 标签控制编码名，字段仍然需要导出才能被常规 JSON 编解码使用。`Decode(&settings)` 接收地址，因为它需要修改结构体。未知字段是否拒绝、尾部是否允许第二个 JSON 值、缺失字段是否有默认值，都属于具体协议的决定；本例选择严格的单对象设置文件。

解析成功不等于业务数据合法，所以还要检查 `Retries` 的范围。使用 `any` 解码数字时，传统 `encoding/json` 默认使用 `float64`，大整数可能损失精度；有确定结构时优先用明确的 Go 类型，动态数据需要时可了解 `Decoder.UseNumber`。

本文使用熟悉且稳定的 `encoding/json` API。Go 1.27.3 中也能读到 `encoding/json/v2` 和 `encoding/json/jsontext`，它们的选项与部分默认行为不同。阅读库源码时应先确认导入路径，不要假定 nil 集合、字段匹配等规则完全一致；导入别名如 `jsonv1 "encoding/json"` 只是让两套 API 可以在同一文件中区分。

### HTTP：连接复用、状态码和资源上限

`http.Client` 可以复用，也支持并发使用。不要为了每个请求反复创建全新 Transport。请求有三个需要分别处理的结果：请求本身失败、服务器返回非成功状态码、成功响应体在读取时失败。`Do` 不会因为 HTTP 404 自动返回错误。

下面是完整程序；它用本机测试服务器演示，不访问互联网。`context` 的取消语义在第 11 节展开。

```go
// tutorial: http
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

func fetchText(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch text: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	const maxBytes = 4096
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxBytes {
		return "", fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	return string(data), nil
}

func main() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	text, err := fetchText(ctx, server.Client(), server.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(text)
}
```

成功路径读取到 EOF 后关闭响应体，有利于连接复用。示例在不接受状态码时直接关闭；实际客户端可以根据响应大小和业务约定决定是否有限度地读完错误响应，不能为了复用连接无限读取。

路径处理也有不同层次：`filepath` 用于操作系统文件路径，`path` 用于斜杠分隔的逻辑路径。`filepath.Join` 和 `Clean` 负责组合或规范化，不会自动提供安全隔离；处理用户输入时仍要限制访问范围。现代 Go 的 `os.Root` 可用于基于目录的受限文件操作，应按其文档理解边界。

启动进程时使用 `exec.CommandContext(ctx, program, arg1, arg2)` 传递参数，不要把用户输入拼成 `sh -c` 字符串。`CommandContext` 管理它启动的进程，但不自动保证清理所有后代进程；进程组、管道关闭和平台行为需要单独设计。

## 10. 并发与同步

### goroutine 与 Promise 的差别

`go f()` 启动一个并发执行的 goroutine。它不是一个返回值可以被 `await` 的 Promise，也不会自动告诉调用方完成或失败。要得到结果，需要显式设计 channel、等待或其他同步方式。

Go 调度器可以把 goroutine 分配到多个 OS 线程上，它们可能并行执行。JavaScript 主线程上的“这段代码中间没有 await，所以不会与另一段 JS 并发访问”不能直接迁移到 Go。

并发表示任务在时间上可以交错推进；并行表示同一时刻实际执行多个任务。goroutine 很轻，但也有栈、调度和资源成本；网络请求、文件描述符、内存和服务端配额仍然要求限制并发数。

### channel 表达数据交接

完整程序：

```go
// tutorial: channels
package main

import (
	"fmt"
	"sync"
)

func main() {
	jobs := make(chan int)
	results := make(chan int)
	var workers sync.WaitGroup

	for range 2 {
		workers.Go(func() {
			for value := range jobs {
				results <- value * value
			}
		})
	}

	go func() {
		defer close(jobs)
		for value := 1; value <= 4; value++ {
			jobs <- value
		}
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	total := 0
	for result := range results {
		total += result
	}
	fmt.Println(total)
}
```

输出 `30`，单个结果到达的顺序没有保证。`WaitGroup.Go` 启动并记录任务，任务返回时完成计数；传入函数不应 panic。阅读旧代码还会遇到 `Add`、`go`、`defer Done()` 的组合。手动使用 `Add` 时应在启动 goroutine 之前增加计数，不能让 `Wait` 先看到零个任务就返回。

无缓冲 channel 的发送与接收需要配对；有缓冲 channel 在缓冲未满时允许发送方先行。channel 是同步机制，成功的数据交接建立跨 goroutine 的可见性关系，不能把它仅理解为普通数组队列。

`chan<- T` 只允许发送，`<-chan T` 只允许接收，用于限制函数可做的操作。关闭权应属于能够确认“所有发送已经结束”的一方。多生产者情况下，通常由等待所有生产者结束的协调者关闭，不能每个生产者都 `defer close(ch)`。

| 操作 | 行为 |
| --- | --- |
| 接收已关闭且已排空的 channel | 立即得到零值，二值接收的 `ok` 为 false |
| `for value := range ch` | 持续接收，直到 channel 关闭且排空 |
| 向已关闭 channel 发送，或重复关闭 | panic |
| 从 nil channel 接收，或向其发送 | 一直阻塞 |
| 关闭 nil channel | panic |

`select` 等待多个 channel 操作；多个 case 都就绪时不会按源码顺序保证优先级。`default` 表示当前都不能立即执行时直接走该分支，放在没有其他阻塞措施的循环里可能造成忙等。

### 锁适合保护共享状态

完整程序：

```go
// tutorial: mutex
package main

import (
	"fmt"
	"sync"
)

type Counter struct {
	mu sync.Mutex
	n  int
}

func (c *Counter) Add() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *Counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func main() {
	var counter Counter
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				counter.Add()
			}
		})
	}
	workers.Wait()
	fmt.Println(counter.Value())
}
```

输出 `400`。读与写必须遵守同一同步规则；只给写加锁，读时直接访问字段，并不一定安全。锁保护的是一组数据的不变量，而不是“恰好这个函数里看起来危险的那一行”。

`Mutex`、`RWMutex`、`WaitGroup`、`Once` 在使用后不能复制。`RWMutex` 允许多个读锁，但不是天然更快；锁的选择应以访问模式和测量为依据。`sync.Once` 确保一个动作只执行一次；动作失败不会自动让下一次重试，所以它并非通用的重试初始化工具。

`sync/atomic` 提供单个原子操作及对应内存顺序保证，适合独立计数或已严谨设计的状态发布。多个原子字段的组合不会自动形成事务；字段之间有一致性关系时，使用一把锁通常更容易验证。

如果把受锁保护的 map 直接返回给调用方，调用方仍然能在锁外修改它。返回复制后的数据，或提供受控操作，才能把同步边界保持完整。

`go test -race` 和 `go run -race` 可以发现运行中实际经过的数据竞争。一次检查没有报错不等于程序已被证明无竞争；仍需理解共享数据和同步关系。

## 11. 取消、后台任务与接口契约

### context 表达操作的生命周期

`context.Context` 传递取消信号、deadline 和少量与请求相关的数据。一般把它作为可能阻塞或需要取消的函数的第一个参数，命名为 `ctx`。不要传 nil；没有父级上下文时通常使用 `context.Background()`。

`WithCancel`、`WithTimeout`、`WithDeadline` 返回派生 context 和取消函数。`defer cancel()` 及时释放相关资源，即使操作提前成功也应调用。取消函数可以重复调用。

`ctx.Done()` 是取消后关闭的 channel；`ctx.Err()` 返回取消原因类别，如 `context.Canceled` 或 `context.DeadlineExceeded`。需要携带具体原因时，可了解 `WithCancelCause` 与 `context.Cause`，而不是把所有错误塞进 `ctx.Value`。

取消是协作式的：它不会强制杀死任意 goroutine，也不会使一个不支持 context 的阻塞调用自动返回。网络操作可以传递 context；CPU 循环需要主动检查；普通 `io.Reader` 是否能被中断则取决于具体实现。

### 完成、取消和进度是不同信息

下面是完整程序。`Process` 同步执行任务；由调用方决定是否放到后台。它的进度回调顺序执行，返回结果代表最终事实，取消时保留已处理的部分结果。

```go
// tutorial: cancellation
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Progress struct {
	Done  int
	Total int
}

type Result struct {
	Values []string
}

func Process(ctx context.Context, values []string, report func(Progress)) (Result, error) {
	result := Result{Values: make([]string, 0, len(values))}
	for i, value := range values {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Values = append(result.Values, strings.TrimSpace(value))
		if report != nil {
			report(Progress{Done: i + 1, Total: len(values)})
		}
	}
	return result, nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := Process(ctx, []string{" one ", " two ", " three "}, func(p Progress) {
			fmt.Printf("%d/%d\n", p.Done, p.Total)
			if p.Done == 2 {
				cancel()
			}
		})
		done <- outcome{result: result, err: err}
	}()

	finished := <-done
	fmt.Println(finished.result.Values)
	fmt.Println(errors.Is(finished.err, context.Canceled))
}
```

输出：

```text
1/3
2/3
[one two]
true
```

主 goroutine 不只是调用 cancel 后离开，而是从 `done` 接收最终结果，这也保证后台任务已经完成了它的清理和返回。拥有 goroutine 的代码应当知道它何时退出、如何报告错误，以及调用方提前离开时它是否仍可能阻塞。

本例回调是同步的，所以慢回调会拖慢任务。如果真实接口允许多 worker 并发产生进度，需要明确是否串行化回调、事件是否有序、是否允许阻塞、消费者退出后怎么办。字节进度通常可以合并；最终成功或失败不能只靠一个可能丢失的进度事件表达。

有界队列可以限制积压内存，但队列满时选择等待、合并或丢弃，仍然是契约的一部分。不要在持有关键状态锁时调用任意外部回调，否则回调可能再次进入同一组件或等待另一线程，引发死锁。

### 把副作用和呈现分开

可复用的核心函数应接收明确输入，返回类型化结果和错误；它不应自行决定打印到终端、弹窗或结束进程。调用方负责呈现、信号和交互，核心负责自己的数据一致性。

`context` 不能代替事务。如果任务正在更新多个文件，取消时要么在安全点停止，要么完成必要的提交或恢复。某个路径的原子 rename 不等于多个文件组成的操作天然原子。

同样，`sync.Mutex` 只协调同一进程中的 goroutine。多个独立进程操作同一状态时，需要操作系统锁或其他跨进程协调；把核心代码放进一个共享 Go 包不会改变这一点。

## 12. 阅读与验证真实仓库

### 从入口和边界开始

拿到一个陌生仓库，先读 `go.mod`，了解 Go 版本、直接依赖和 module 路径；再看 `cmd` 或 `package main`，找到入口如何组装依赖。随后沿一个具体行为阅读其输入、调用链、副作用和返回结果。

不必先逐行读完底层实现。遇到不熟悉的类型，先看公开方法、注释、调用方和测试；它们共同说明这个类型的契约。包之间的依赖不允许形成环，出现循环依赖往往说明边界需要调整，而不是增加一个全局注册表就能解决。

常用只读命令：

```sh
go list ./...
go doc -all ./某个包目录
go list -deps ./某个入口目录
go env GOOS GOARCH GOVERSION
```

路径中的“某个包目录”需要替换为当前仓库的实际路径。命令行和编辑器的定义跳转可以互补：先判断“这个值是什么类型、谁创建、谁关闭、谁修改”，通常比追逐所有函数实现更快。

### 看懂平台文件、初始化与生成代码

| 形式 | 阅读时需要知道的事 |
| --- | --- |
| `file_darwin.go`、`file_linux.go`、`file_arm64.go` | 文件名自动限制目标 OS 或 CPU 架构 |
| `//go:build ...` | 构建约束进一步决定文件是否参与构建；位于文件开头 |
| `init()` | 包初始化期间运行，早于依赖它的包的初始化和 `main` |
| `_ "some/package"` | 仅为初始化副作用而导入，没有在本文件中可引用的包名 |
| `//go:embed` | 编译时把匹配的资源放进程序；读取运行时磁盘文件是另一件事 |
| `//go:generate` | 为 `go generate` 声明命令，`go build` 不会自动执行这些命令 |
| `tool` 依赖 | 让开发工具版本随 module 管理，通常用 `go tool 工具名` 调用 |
| `Code generated ... DO NOT EDIT.` | 修改生成源或生成器，再运行相应命令，不直接维护输出文件 |

`init` 适合必须发生的局部初始化，不适合隐藏网络请求、启动难以停止的后台任务，或让导入一个包就改变整个应用的外部状态。阅读框架时可能遇到必须占用主线程的初始化，这是特定平台约束，不是所有业务包的默认模式。

### 反射：知道它在检查类型还是修改值

反射是运行时检查类型与值的机制。`reflect.Type` 描述类型；`reflect.Value` 包装一个具体值；`Kind` 是较粗的类别，例如 struct、slice、ptr，不能代替完整类型身份。

完整程序：

```go
// tutorial: reflection
package main

import (
	"fmt"
	"reflect"
)

type Record struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func main() {
	typ := reflect.TypeFor[Record]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		fmt.Println(field.Name, field.Tag.Get("json"))
	}

	record := Record{Name: "Ada", Age: 36}
	value := reflect.ValueOf(&record).Elem()
	age := value.FieldByName("Age")
	if age.IsValid() && age.CanSet() && age.Kind() == reflect.Int {
		age.SetInt(37)
	}
	fmt.Println(record.Age)
}
```

`ValueOf(record)` 包装的是传入的副本，不能直接设置原变量；`ValueOf(&record).Elem()` 才得到原结构体的可设置值。反射操作必须尊重有效性、类型、导出性和可设置性，否则可能 panic。

绑定服务、生成类型或序列化库可能利用反射检查方法和标签。普通业务代码有明确类型时，直接调用方法更简单；不必模仿框架底层把所有操作都变成反射。

### 必要的测试验证行为

Go 内置的 `testing` 已足以完成大量测试。测试文件以 `_test.go` 结尾，测试函数是 `func TestXxx(t *testing.T)`。同包测试可以访问未导出符号；使用 `package name_test` 的外部测试更接近真实调用方。

以下是完整测试文件，保存为 `words_test.go`。为了能够独立运行，把被测的两个小函数也放在同一个代码块里；真实工程通常把它们放在普通 `.go` 文件中。

```go
// tutorial: testing test
package words

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Count(text string) int {
	return len(strings.Fields(text))
}

func CountFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("count file %q: %w", path, err)
	}
	return Count(string(data)), nil
}

func TestCount(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{name: "empty", input: "", want: 0},
		{name: "whitespace", input: " Go\tand\nRust ", want: 3},
		{name: "unicode", input: "你好 世界", want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Count(tt.input); got != tt.want {
				t.Fatalf("Count(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestCountFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("one two"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := CountFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("CountFile() = %d, want 2", got)
	}

	_, err = CountFile(filepath.Join(dir, "missing.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file error = %v, want fs.ErrNotExist", err)
	}
}
```

在包含该文件的 module 中执行：

```sh
go test ./...
go test -race ./...
go vet ./...
```

`t.Run` 给不同场景命名；`t.TempDir` 自动清理临时目录；`t.Cleanup` 适合注册其他测试资源的清理。HTTP 客户端可使用前面的 `httptest` 创建本地服务器，避免测试依赖公共网络状态。

测试应优先覆盖输入边界、错误传播、资源释放、持久化一致性和并发行为。没有必要为每个字段、简单转发方法或标准库自身复制一套测试。测试名称和断言应描述行为，不要锁死与使用者无关的内部调用顺序。

需要验证性能时，先定义工作负载与冷/热缓存条件，再用 benchmark 或 profile 定位成本。当前 Go 可以在 `BenchmarkXxx` 中使用 `b.Loop()`；不要把一次网络下载的耗时当成稳定的 CPU benchmark，也不要仅为提高覆盖率增加重复测试。

### 主线程、unsafe 与 FFI 的阅读边界

大部分应用代码只需要普通 Go 类型、接口和并发机制。读取桌面或系统框架时，还会遇到以下内容：

- `runtime.LockOSThread` 把当前 goroutine 固定到当前 OS 线程。某些 UI 工具包要求主线程运行事件循环，因此框架会做特殊初始化；goroutine 本身并不天然绑定主线程。
- `unsafe.Pointer` 允许绕过一部分类型系统操作地址；`uintptr` 是整数，不会仅因为数值看起来像地址就让垃圾回收器保持目标对象存活。转换规则和生命周期必须遵守 Go 的专门约束。
- FFI 负责跨语言或系统 ABI 调用，可能涉及动态库、符号地址、调用约定和 callback。即使框架没有使用 cgo，也可能通过运行时桥接调用原生库。
- Go 的 GC 不会自动管理所有原生对象。原生对象可能需要 retain/release，callback 可能需要保持有效，某些对象只能在指定线程访问。`defer` 可以组织清理，但不会凭空知道原生 API 的所有权规则。
- 直接修改可执行文件、图形后端或汇编 trampoline 属于专门实现层。阅读上层业务与公开 API 时，先确认该层的输入、输出、线程和生命周期约束即可。

阅读 MyGo 这类框架时，可以先看公开类型、生命周期、事件回调和视图函数，再进入平台实现。它的 native UI 由 Go 代码直接调用；方法链、泛型事件和闭包仍然遵循本文的 Go 规则。长任务不应阻塞界面事件线程，后台结果应通过框架提供的线程切换方法更新界面状态。

## 参考资料

- [Go 语言规范](https://go.dev/ref/spec)：类型、方法集、接口、赋值与控制流的准确规则。
- [Go modules 参考](https://go.dev/ref/mod)：module、版本选择、`go.mod` 与工具链。
- [Go 内存模型](https://go.dev/ref/mem)：goroutine 间可见性与同步保证。
- [标准库文档](https://pkg.go.dev/std)：接口契约、错误语义和示例应以具体包文档为准。
- [context](https://pkg.go.dev/context)、[sync](https://pkg.go.dev/sync)、[iter](https://pkg.go.dev/iter)：取消、同步和迭代器。
- [unsafe](https://pkg.go.dev/unsafe)：指针转换允许的模式及限制。
- [MyGo 架构文档](https://github.com/egoist/mygo/blob/v0.6.4/docs/architecture.md)：作为后续阅读真实 Go 框架的材料，注意它的版本与平台约束。
