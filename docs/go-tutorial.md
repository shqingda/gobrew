# 面向 JavaScript / TypeScript 开发者的 Go 入门

这篇教程写给已经用 JavaScript、TypeScript、React 或 Node.js 做过产品的开发者，目标是让你能读懂并修改真实的 Go 工程。内容按“先会写、再写对、最后读懂别人的代码”的顺序展开：从语言基础开始，然后是资源管理和错误处理，接着是并发，最后讲如何阅读一个陌生仓库。文中以 **Go 1.27.3** 为准，示例只用标准库。

Go 的语法很少，一两天就能看完。真正容易出错的地方在于：它对值、接口、错误和并发的处理方式和 JavaScript 很不一样。如果只是把熟悉的 JS 写法逐句翻译成 Go，代码往往能编译，却在复制、共享或并发时出现意料之外的行为。所以本文会反复对比两者的差异，并解释 Go 为什么这样设计。

### 如何运行示例

标为“完整程序”的代码块都可以单独保存为 `main.go` 运行，不需要和其他代码块拼在一起；标为“完整测试文件”的代码块保存为 `words_test.go`。每个代码块第一行的 `// tutorial: ...` 注释是给校验脚本用的标记，不影响运行。

如果你在本仓库中阅读，可以在根目录执行 `go run ./scripts/check-tutorial.go`。它会在临时目录里编译、运行并检查文中所有 Go 示例，不需要下载依赖。

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

先看最小的完整程序：

```go
// tutorial: hello
package main

import "fmt"

func main() {
	fmt.Println("你好，Go")
}
```

每个 Go 文件都以 `package` 开头，声明自己属于哪个包。名为 `main` 的包比较特殊：它会被编译成可执行程序，程序从其中的 `func main()` 开始运行。

`import "fmt"` 引入标准库的格式化输出包，之后就能调用它的 `fmt.Println`。注意 `Println` 是大写开头：Go 没有 `export` 关键字，而是用首字母大小写决定一个名字能否被其他包访问。大写开头的名字是公开的，小写开头的只在本包内可见。

在一个新建的空目录中运行下面的命令：

```sh
go mod init example.com/hello
go run .
go build -o hello .
./hello
```

第一条命令创建 module（下一小节解释）。`go run .` 会先编译再运行，适合开发时快速试验；`go build` 则产出一个可执行文件，之后可以直接运行，不需要安装 Go。

这一点和 Node.js 不同：Node 需要解释器在运行时执行源码，而 Go 在构建时就编译成了本机机器码。不过编译产物里仍然带着 Go runtime，它负责 goroutine 调度、垃圾回收等工作，所以 Go 程序并不像 C 程序那样“裸跑”。

### module 和 package 的分工

刚才的 `go mod init` 创建了一个 **module**。module 和 package 是两个层级的概念，初学时容易混淆：

- **package（包）** 是代码组织和可见性的单位。一个目录就是一个包，目录里的所有 `.go` 文件共享同一组包级声明——在 `a.go` 里定义的函数，`b.go` 可以直接调用，不需要导入。这和 ES module 不同：在 JS 里每个文件都是独立作用域。唯一的例外是 `import`：导入只对写它的那个文件生效，`a.go` 导入了 `fmt`，`b.go` 要用的话也得自己导入。
- **module（模块）** 是版本管理和发布的单位，由根目录的 `go.mod` 文件定义，包含一个或多个包。它大致相当于一个带 `package.json` 的 npm 包。

一个最小的 `go.mod` 长这样：

```text
module example.com/hello

go 1.27.3
```

`module` 一行声明模块路径，它会成为模块内所有包导入路径的前缀。路径看起来像网址，但练习项目不需要真的发布到这个域名。

`go` 一行声明这个模块要求的最低 Go 版本。它和 `package.json` 里的 `engines` 字段有些像，但作用更强：不同的版本号会改变部分语言语义（第 5 节会提到一个例子）。如果本机的 Go 版本低于这里的要求，工具链可能会自动下载合适的版本；不希望这样时，可以设置环境变量 `GOTOOLCHAIN=local`。

Go 工程里还有几个常见的文件和目录：

| 文件或目录 | 作用 |
| --- | --- |
| `go.mod` | 模块路径、最低 Go 版本和依赖列表 |
| `go.sum` | 已下载依赖的内容校验值，用来防止依赖被篡改；它的作用和 `package-lock.json` 并不完全相同 |
| `internal/` | 放在这里的包只能被其父目录及其子目录中的代码导入，用来隐藏不想公开的实现 |
| `cmd/<程序名>/` | 存放可执行程序入口的惯用位置，是社区约定而不是编译器要求 |
| `*_test.go` | 测试文件，只在 `go test` 时编译，不会进入正式构建 |

管理依赖时，用 `go get 模块路径@版本号` 添加或升级依赖，用 `go mod tidy` 根据代码中实际的导入补全缺失依赖、移除多余依赖。Go 选择依赖版本的规则叫“最小版本选择”：它会选用满足所有要求的最低版本，而不是像 npm 那样倾向于范围内的最新版本，因此构建结果更稳定。

遇到看不懂的依赖错误时，不要手动删除 `go.sum` 来“修复”。它记录的是校验值，删掉只会掩盖问题，通常 `go mod tidy` 才是正确的修复手段。

### 日常工具

Go 自带了开发中最常用的工具，不需要另外安装 ESLint、Prettier 或 Jest 这类东西：

```sh
go version
go fmt ./...
go vet ./...
go test ./...
go doc io.Reader
go list ./...
go mod tidy
```

`./...` 表示“当前目录及所有子目录中的包”。其中最常用的三个命令各管一件事：`go fmt` 统一代码格式，`go vet` 检查编译能通过但很可能有问题的写法，`go test` 运行测试。

另外要提前知道：Go 编译器比 TypeScript 严格，**未使用的导入和局部变量会直接导致编译失败**，而不只是警告。刚开始写 Go 时，这会是最常见的编译错误。

## 2. 变量、类型与控制流

### 零值和类型推导

Go 是静态类型语言，每个变量在编译时就有确定的类型。和 JavaScript 不同，Go 里没有 `undefined`：声明了但没赋值的变量会自动得到它所属类型的**零值**——数字是 `0`，布尔是 `false`，字符串是 `""`，指针、slice、map、channel、函数和接口是 `nil`，结构体则是每个字段各自取零值。Go 的很多 API 都会刻意设计成“零值即可用”，后面会多次看到。

声明变量有两种常见写法：

- `var count int`：显式声明类型，变量取零值。
- `count := 3`：声明并赋值，类型由右侧推导。这种写法只能用在函数内部。

`:=` 是“声明”，不是“赋值”，所以它要求左侧至少有一个新变量；给已有的变量重新赋值要用 `=`。

`const` 在 Go 里的含义也和 JS 不同。JS 的 `const` 表示“这个绑定不能再指向别的值”，但对象内容仍然可以修改；Go 的 `const` 则只能用于数字、字符串、布尔这类编译期就能确定的常量。Go 没有办法把 slice、map 或结构体声明成不可变的。

下面这个程序把变量、错误处理、循环和 `switch` 放在了一起：

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
		// 保留底层错误，调用方仍可识别转换失败的原因。
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

程序输出 `positive: 3`。下面按代码顺序解释。

**多返回值。** `parseLimit` 返回 `(int, error)` 两个值。JS 里要返回多个值，通常得包成数组或对象再解构；Go 直接支持多返回值，调用方用 `limit, err :=` 分别接收。“结果加错误”是 Go 最常见的函数签名：成功时 `err` 为 `nil`，失败时调用方检查 `err` 并处理。如果某个返回值用不到，可以用 `_` 丢弃它，但不要习惯性地用 `_` 丢掉错误。

**循环。** Go 只有 `for` 一种循环关键字，但它有好几种写法：

- `for i := range limit`：从 `0` 数到 `limit - 1`，本例中依次得到 `0`、`1`、`2`，所以 `total` 是 `3`。
- `for i := 0; i < limit; i++`：和 JS 一样的传统三段式。
- `for 条件 { }`：相当于 `while`。
- `for { }`：无限循环。

**条件判断。** `if` 和 `switch` 的条件必须是 `bool` 类型。Go 没有 truthy/falsy 的概念，`if count` 这种写法无法编译，必须写成 `if count != 0`。

**switch。** 不写判断对象的 `switch` 相当于一串 `if / else if`。和 JS 不同，Go 的每个 `case` 执行完会自动结束，不会贯穿到下一个分支，所以不需要写 `break`。

### 类型转换必须显式进行

JavaScript 会在很多场合悄悄转换类型，比如 `"1" + 2` 得到 `"12"`。Go 完全不做隐式转换，即使是 `int` 和 `int64` 这样都表示整数的类型，也不能直接相加，必须先写 `int64(count)` 显式转换。

显式转换只是让编译通过，并不会检查数值是否合法。把一个大的 `int64` 转成 `int8` 时，超出范围的部分会被直接截断。所以在处理外部输入时，仍然要自己检查范围。

基于已有类型，可以定义新类型：

- `type UserID string` 定义一个**新类型**。它的底层是字符串，但编译器把它和 `string` 视为不同类型，不能混用。这可以防止把普通字符串误传给需要用户 ID 的参数。
- `type UserID = string`（多了一个等号）定义的是**别名**，两者完全是同一种类型。别名主要用于代码迁移或重新导出类型。

### 小心变量遮蔽

`:=` 带来一个常见陷阱。`if` 语句可以在条件前先写一个初始化语句，例如 `if n, err := parseLimit(s); err != nil { ... }`，这里的 `n` 和 `err` 只在这个 `if` 内有效。如果外层已经有同名变量，`:=` 会在内层新建一个变量来**遮蔽**外层的那个，而不是给外层变量赋值。

结果就是：你以为更新了外层的 `n`，实际上它一直保持旧值。遇到需要更新外层变量的情况，应该提前声明好变量，然后在内层用 `=` 赋值。

## 3. 值、指针与共享数据

### 赋值和传参都会复制值

在 JavaScript 里，`const b = a` 之后，如果 `a` 是对象，`a` 和 `b` 就指向同一个对象，修改 `b.name` 会影响 `a`。Go 的规则不同：结构体是值，赋值或传参时会把整个结构体复制一份。想让函数修改调用方的结构体，必须显式地传递指针。

下面的例子同时展示了复制和共享：

```go
// tutorial: values
package main

import "fmt"

type Profile struct {
	Name string
	Tags []string
}

func rename(p *Profile, name string) {
	// 参数复制了地址；字段赋值通过这个地址修改调用方的数据。
	p.Name = name
}

func main() {
	a := Profile{Name: "Ada", Tags: []string{"reader"}}
	// 复制整个结构体，但其中的 slice 仍指向同一个底层数组。
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

第一行输出值得仔细看。`b := a` 复制了结构体，所以修改 `b.Name` 不影响 `a.Name`，`a` 仍然是 `Ada`。但修改 `b.Tags[0]` 却改到了 `a` 的数据，这是因为 `Tags` 是一个 slice。slice 本身只是一个“指向底层数组的视图”（第 4 节详细讲），复制 slice 只复制了这个视图，没有复制底层数组，所以 `a.Tags` 和 `b.Tags` 仍然指向同一块数据。

换句话说，Go 并没有“结构体有时按值、有时按引用”的特殊规则。复制始终是复制值，只不过有些值（slice、map、指针等）本身就引用着别处的数据，复制之后两份副本仍然引用同一处。

再看指针相关的语法：

- `&a` 取变量 `a` 的地址，得到一个 `*Profile` 类型的指针。
- `*p` 解引用，取得指针指向的值。
- 通过指针访问字段时可以直接写 `p.Name`，Go 会自动解引用，不需要写成 `(*p).Name`。
- 指针的零值是 `nil`。对 `nil` 指针解引用会触发 panic（程序崩溃），类似 JS 中访问 `null` 的属性报错。

最后要注意，指针本身也是按值传递的：`rename` 收到的是地址的一份副本。所以函数可以通过这个地址修改它指向的数据，却无法让调用方的指针变量改为指向别的东西。

### 不需要手动管理栈和堆

如果你学过 C，可能会担心“返回局部变量的地址会不会导致悬空指针”。在 Go 中这完全合法：编译器会通过逃逸分析判断变量应该放在栈上还是堆上，垃圾回收器负责在没人引用时回收它。你不需要为此把变量改成全局变量。

也不要把“用指针”当成默认的性能优化。指针意味着多处代码可以修改同一份数据，会让程序更难推理；对于小结构体，按值传递往往更清晰，性能也未必更差。是否用指针，应该根据“是否需要共享和修改”来决定；如果是为了性能，就以实际测量为准。

### 复制不等于深拷贝

前面的例子已经说明，结构体赋值只复制一层。标准库的 `slices.Clone` 和 `maps.Clone` 也是如此：它们复制了容器本身，但如果元素里还有指针、slice 或 map，这些内层数据仍然是共享的。Go 没有通用的深拷贝函数，需要完全独立的副本时，要根据数据结构自己逐层复制。

## 4. 字符串与集合

### 字符串是字节序列，不是字符数组

在 JavaScript 里，字符串按 UTF-16 编码单元计数，`"语".length` 是 `1`。Go 的字符串则是**不可变的字节序列**，通常存放 UTF-8 编码的文本（但类型本身不保证内容一定是合法的 UTF-8）。这导致几个直觉上的差异：

- `len(s)` 返回的是**字节数**，不是字符数。一个中文字符在 UTF-8 中占 3 个字节。
- `s[i]` 取到的是第 `i` 个字节（`byte` 类型），而不是第 `i` 个字符。
- `for i, r := range s` 会按 UTF-8 解码字符串，每次得到一个 `rune`（Unicode 码点）和它在字符串中的字节偏移 `i`。

`byte` 是 `uint8` 的别名，`rune` 是 `int32` 的别名。还要注意，一个 rune 并不总是对应用户眼中的“一个字符”：带组合符号的字母、由多个码点拼成的 emoji，都由多个 rune 组成。

### slice：长度、容量和底层数组

Go 有数组和 slice 两种顺序集合。数组 `[3]int` 的长度是类型的一部分，`[3]int` 和 `[4]int` 是不同的类型，所以数组很少直接使用。日常使用的是 slice `[]int`，它相当于 JS 的数组，但内部结构不同：slice 本身只是一个小的描述结构，记录着“指向哪个底层数组、从哪里开始、长度多少、容量多少”，真正的数据存放在底层数组里。

理解了这一点，就能解释 slice 的大多数行为。看例子：

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
	// view 是视图；修改元素会反映到 items 中。
	view := items[:2]
	view[0] = 99
	// int 元素没有更深层的引用，因此此处 Clone 足以隔离修改。
	independent := slices.Clone(view)
	independent[0] = 7
	fmt.Println(items, independent)

	var names []string
	// append 可能分配新数组，要接收它返回的 slice。
	names = append(names, "Ada")
	fmt.Println(names, len(names))

	counts := make(map[string]int)
	counts["go"]++
	// exists 区分“键不存在”和“键存在但值恰好为零”。
	count, exists := counts["rust"]
	fmt.Println(count, exists)
	delete(counts, "go")
}
```

先看字符串部分：`"Go语言"` 有 2 个 ASCII 字符（各 1 字节）和 2 个中文字符（各 3 字节），所以 `len(s)` 是 `8`，转换成 rune 后数量是 `4`。遍历时得到的字节偏移依次是 `0`、`1`、`2`、`5`。

再看 slice 部分：`items[:2]` 并没有复制数据，它创建了一个指向同一底层数组的新视图，所以修改 `view[0]` 会把 `items[0]` 也变成 `99`。如果需要独立的副本，就用 `slices.Clone`。这里元素是 `int`，没有更深层的引用，所以 Clone 之后修改 `independent` 不会影响原数据。

### append 必须接收返回值

`append` 向 slice 末尾添加元素。如果底层数组还有剩余容量，`append` 会直接写进去；如果容量不够，它会分配一个更大的新数组，把旧数据复制过去。无论哪种情况，结果都通过返回值给出，所以必须写成 `items = append(items, value)`。

由于“是否分配新数组”取决于当时的容量，两个共享底层数组的 slice 在 append 之后，可能仍然共享数据，也可能已经分开。这种行为很难预测，所以不要依赖 append “碰巧”分配新数组来隔离数据；需要隔离时，就显式复制。

`make` 可以预先创建 slice，但要分清两种写法：

- `make([]T, n)` 创建长度为 `n` 的 slice，里面已经有 `n` 个零值元素。
- `make([]T, 0, n)` 创建长度为 `0`、容量为 `n` 的 slice，只是预留了空间。

一个常见错误是用第一种写法创建 slice，再 append `n` 次，结果得到 `2n` 个元素，前一半都是零值。如果打算用 append 填充，应该用第二种写法。

### map 的基本用法

map 相当于 JS 的 `Map` 或用作字典的对象。例子中的 `counts["go"]++` 能直接工作，是因为读取不存在的键会返回零值 `0`，再加一即可。

但这也带来一个问题：读到 `0` 时，无法区分“键不存在”和“键存在但值恰好是 0”。所以 Go 提供了双返回值写法 `count, exists := counts["rust"]`，第二个值表示键是否存在。

### nil 与空集合

slice 和 map 的零值都是 `nil`，但它们对 nil 的容忍程度不同：

- nil slice 可以正常求 `len`、`range` 遍历和 `append`，行为和空 slice 一样，所以通常直接用 `var names []string` 声明即可。
- nil map 可以读取、求长度、遍历和 `delete`，但**不能写入**，写入会 panic。所以 map 在写入前必须用 `make` 或字面量初始化。

虽然 nil slice 和空 slice 在大部分操作中表现一样，但在序列化时可能不同。例如传统的 `encoding/json` 会把 nil slice 编码为 `null`，把空 slice 编码为 `[]`。如果前端代码依赖返回的是数组，这个差别就会造成 bug。因此在定义 API 输出时，需要明确选择其中一种。

关于集合，还有几条规则需要记住：

- map 的遍历顺序是随机的。需要稳定的输出时，先取出键排序，再按顺序访问。
- map 不能在多个 goroutine 中同时读写，否则会出错（第 10 节讲如何加锁）。
- slice 和 map 不能用 `==` 比较内容，只能和 `nil` 比较。要比较内容，可以用 `slices.Equal` 或 `maps.Equal`，前提是元素类型本身可比较。

## 5. 函数、闭包与资源生命周期

### 函数是一等值

和 JavaScript 一样，Go 的函数也是值，可以赋给变量、作为参数传递。函数类型写作 `func(string) bool`，参数名不属于类型的一部分。

在 JS 里，回调函数非常常见；在 Go 里也一样。只需要一个回调时，直接接受一个函数参数就够了，不必专门定义一个接口。下面的 `filter` 就是这样：

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
	// 闭包捕获 prefix 变量；若之后修改它，后续调用会读到新值。
	keep := func(s string) bool {
		return strings.HasPrefix(s, prefix)
	}
	fmt.Println(filter([]string{"go", "rust", "gopher"}, keep))

	n := 1
	// 参数现在求值，调用要等到 main 返回前才发生。
	defer fmt.Println("argument:", n)
	defer func() {
		// 函数体届时才读取 n；后注册的 defer 先执行。
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

`keep` 是一个闭包，它引用了外部的 `prefix`。和 JS 一样，闭包捕获的是**变量本身**，而不是变量当时的值。如果之后修改了 `prefix`，再调用 `keep` 就会读到新值。

程序后半部分用到了 `defer`，下一小节详细介绍。这里先解释输出顺序为什么是这样：

1. `defer` 会把一个调用推迟到当前函数返回前才执行，多个 defer 按“后注册先执行”的顺序运行。所以 `closure` 那一行先输出。
2. `defer fmt.Println("argument:", n)` 的参数在执行 `defer` 语句**那一刻**就求值了，当时 `n` 是 `1`，所以最终输出 `argument: 1`。
3. 第二个 defer 注册的是一个闭包，函数体直到 `main` 返回前才运行，那时 `n` 已经变成 `2`，所以输出 `closure: 2`。

### 用 defer 释放资源

JS 里释放资源通常写 `try { ... } finally { file.close() }`。Go 用 `defer` 达到同样的效果：拿到资源后立刻写一行 `defer file.Close()`，无论函数从哪个 `return` 退出，关闭操作都会执行。常见的用法还有 `defer mu.Unlock()`（释放锁）和 `defer cancel()`（取消 context，见第 11 节）。这样写的好处是获取和释放挨在一起，不容易遗漏。

使用 defer 时有几点要注意：

**先检查错误，再注册 defer。** 如果打开文件失败，拿到的 `file` 是无效的，对它调用 `Close` 没有意义。正确的顺序是：获取资源，检查错误，出错就返回；成功后再写 `defer`。

**defer 属于函数，不属于代码块。** defer 的调用要等到整个函数返回时才执行，而不是在当前 `if` 或 `for` 块结束时。如果在一个循环里反复打开文件并 `defer Close()`，所有文件都要等到函数结束才关闭，循环次数一多就可能耗尽文件描述符。解决办法是把每一轮的处理提取成一个独立函数，让 defer 在每轮结束时就执行。

**不要忽略 Close 的错误。** 写文件时，数据可能先进入缓冲区，到 `Close` 时才真正写到磁盘，因此写入失败可能在 `Close` 时才报告出来。`defer file.Close()` 会丢掉这个错误，所以写文件时应该检查 `Close` 的返回值。读文件时，关闭失败通常不影响已经读到的数据，问题不大。

### 可变参数

函数可以接受不定数量的参数，写作 `func logValues(values ...string)`。在函数内部，`values` 就是一个 `[]string`。调用时可以传入任意个字符串，也可以用 `logValues(existing...)` 把一个已有的 slice 展开传入，类似 JS 的展开运算符。

### 循环变量的语义变化

在 Go 1.22 之前，`for i := ...` 循环的所有轮次共用同一个变量 `i`，在循环里创建的闭包最后看到的都是同一个值，这是一个经典陷阱（JS 中用 `var` 声明循环变量也有同样问题）。从 Go 1.22 开始，用 `:=` 声明的循环变量每一轮都是独立的新变量，这个陷阱已经不存在了。这也是第 1 节提到的“`go.mod` 中的版本号会改变语言语义”的一个例子。

但这个改动只解决了循环变量本身。如果闭包访问的是循环外的同一个 slice、map 或指针，它们依然是共享的；在并发场景下，仍然要检查数据竞争。

## 6. 结构体、方法与组合

### 方法属于类型

Go 没有 `class`。在 JS 中，class 把数据和行为打包在一起；Go 把两者拆开：结构体只负责存储数据，方法则单独定义并“挂”在类型上。Go 也没有构造函数，惯例是写一个普通函数 `NewX` 来创建实例。如果类型的零值就能直接使用，甚至连 `NewX` 都不需要。

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
	// 值接收者得到副本；调用方只有接收返回值才会得到修改结果。
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

输出 `Visits 2` 和 `HELLO hello`。

方法定义中 `func` 后面括号里的 `(c *Counter)` 叫做**接收者**，作用相当于 JS 的 `this`，但需要你显式命名，并选择是值还是指针：

- **指针接收者** `(c *Counter)`：方法拿到的是原对象的地址，可以修改原对象。`Add` 需要改变计数，所以用指针接收者。
- **值接收者** `(c Counter)`：方法拿到的是一份副本，对副本的修改不会影响原对象。`Value` 只读取数据，用值接收者就够了。

`Label.Upper` 是值接收者，它修改的是副本，然后把副本返回。所以 `label.Upper().Text()` 得到 `HELLO`，而原来的 `label` 仍然是 `hello`。

这提醒我们：看到链式调用时，不能只看外形就判断它的行为。有的方法返回新值（像这里的 `Upper`），有的修改同一个对象再返回自身，还有的返回一个只在当前调用链中有效的 builder。要搞清楚，需要查看每个方法的接收者类型和返回值。

另外，`Counter` 没有构造函数，`var c Counter` 得到的零值（`n` 为 `0`）可以直接使用。这就是前面说的“零值即可用”的设计。

### 方法集：自动取地址的限制

上面的代码中，`r.Add(2)` 调用的是指针接收者方法，但 `r` 并不是指针。之所以能调用，是因为 `r` 是一个变量（可以取地址），编译器自动把它改写成了 `(&r).Add(2)`。

不过这种自动转换只在直接调用方法时发生，在判断“一个类型是否实现了某个接口”（第 7 节）时不会发生。规则是：

- 类型 `T` 的方法集只包含值接收者的方法。
- 类型 `*T` 的方法集同时包含值接收者和指针接收者的方法。

所以，如果某个接口要求的方法是用指针接收者定义的，那么只有 `*T` 实现了这个接口，`T` 没有。

实际选择接收者时，可以参考以下经验：如果类型内部包含锁、持有文件等资源，或者需要保持对象的唯一身份，应该使用指针接收者，并且在使用后不要复制它。同一个类型的方法最好统一使用一种接收者。本例中 `Counter` 混用两种，只是为了对比演示。

### 嵌入不是继承

`Report` 结构体里写了一个只有类型、没有名字的字段 `Counter`，这叫**嵌入**。嵌入的字段仍然是一个普通字段（字段名就是类型名 `Counter`），但 Go 会把它的字段和方法“提升”到外层，所以可以直接写 `r.Add(2)`，它等价于 `r.Counter.Add(2)`。

这看起来像继承，但本质不同。Go 没有类继承体系，也没有子类重写父类方法的机制。具体来说，如果 `Counter` 的某个方法内部调用了另一个方法，它调用的始终是 `Counter` 自己的版本，即使 `Report` 定义了同名方法也不会被调用。另外，如果嵌入了多个类型且它们有同名方法，这些方法就不会被提升，直接调用会编译失败。

因此，嵌入适合“把一个类型的能力直接暴露给外层”的场景。如果只是想持有一个依赖对象，用普通的命名字段（如 `counter Counter`）会更清晰。

## 7. 接口与错误处理

### 接口描述使用方需要的行为

Go 的接口是一组方法的集合。一个类型只要拥有接口要求的所有方法，就自动实现了这个接口，不需要像 Java 那样写 `implements`。这和 TypeScript 的结构化类型很像：TS 只看对象的形状是否匹配，不看它声明自己是什么。区别在于，Go 接口只描述方法，不能描述字段。

`any` 是 `interface{}`（空接口）的别名。由于没有任何方法要求，任何值都满足空接口，所以 `any` 可以存放任意值。但它和 TypeScript 的 `any` 完全不同：TS 的 `any` 会关闭类型检查，可以随意访问属性；Go 的 `any` 则什么都不能直接做，要使用里面的值，必须先通过类型断言或 type switch 取出具体类型（下面的例子会演示）。

什么时候应该定义接口？Go 社区的经验是：接口由**使用方**根据自己的需要来定义，并且只在确实需要替换实现时才定义。例如，一个函数只需要读取字节，就应该接受 `io.Reader` 接口，这样调用方可以传入文件、网络连接或内存缓冲区。反过来，不需要为每个结构体都配一个同名接口，那只会增加代码量，没有实际用处。

### 接口里的 nil

接口有一个让很多人踩坑的地方：一个接口变量里装的指针是 nil，但接口本身却不等于 nil。看例子：

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
	// nil 指针可以进入方法；能否安全调用取决于方法是否处理它。
	if u == nil {
		return "anonymous"
	}
	return u.DisplayName
}

func main() {
	var pointer *User
	// 接口已有动态类型 *User，即使内部指针为 nil，接口也不等于 nil。
	var named Namer = pointer
	fmt.Println(pointer == nil, named == nil, named.Name())

	if user, ok := named.(*User); ok {
		// 断言成功只说明类型匹配，并不保证取出的指针非 nil。
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

第一行输出 `true false anonymous`。要理解为什么 `named == nil` 是 `false`，需要知道接口值内部存了两样东西：**动态类型**（装进去的值是什么类型）和**动态值**（具体的值）。只有两者都为空时，接口才等于 nil。

这里 `named` 的动态类型是 `*User`，动态值是 nil 指针。类型信息不为空，所以接口不等于 nil。由于接口知道类型，`named.Name()` 仍然能调用到 `*User` 的方法，而这个方法恰好处理了 nil 接收者的情况，所以返回了 `anonymous`。

这个规则最容易在返回 `error` 时造成 bug。`error` 也是接口，如果函数在成功时返回了一个值为 nil 的 `*MyError` 指针，调用方检查 `err != nil` 会得到 `true`，误以为出错了。所以成功时一定要直接 `return nil`。

示例后半部分演示了如何从接口中取出具体值：

- **类型断言** `named.(*User)` 取出具体类型的值。如果只写一个接收变量，类型不匹配时会 panic；写成 `user, ok := named.(*User)` 则不会 panic，而是让 `ok` 为 `false`。注意，断言成功只说明类型匹配，取出的指针仍可能是 nil，所以第二行输出 `true`。
- **type switch** `switch v := value.(type)` 根据动态类型选择分支。在每个分支里，`v` 已经是对应的具体类型，可以直接使用。本例走 `int` 分支，输出 `43`。

### 错误是普通的值

JavaScript 用 `throw` 和 `try/catch` 处理错误。Go 没有异常机制，而是把错误当作普通的返回值：`error` 是一个只要求 `Error() string` 方法的接口，函数通过返回值把错误交给调用方，由调用方决定重试、提示用户还是继续向上返回。

这样做的代价是要写很多 `if err != nil`，好处是每个可能失败的地方都清晰可见，不会有异常从意想不到的地方抛出来。

错误的 `Error()` 文本是给人看的。如果程序需要根据错误种类做不同处理，不应该去比较错误字符串，而应该使用可识别的错误值或错误类型。下面的例子演示了两种做法：

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
	// 让 errors.Is/As 能继续检查被包装的原因。
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
	// As 要更新 input 本身，所以传入指针变量的地址。
	if errors.As(err, &input) {
		fmt.Println(input.Value)
	}
}
```

输出 `true` 和 `70000`。这段代码展示了一个错误如何被层层包装，又如何被识别出来：

1. `ErrInvalidPort` 是一个**哨兵错误**：一个预先定义好的错误值，调用方可以检查错误是否就是它。
2. `InputError` 是一个**自定义错误类型**，它除了错误信息，还携带了出错的输入值 `Value`。它的 `Unwrap` 方法返回内部包装的原因，让外部可以继续往里查。
3. `parsePort("70000")` 返回一个 `InputError`，里面包着 `ErrInvalidPort`。
4. `main` 又用 `fmt.Errorf("read settings: %w", err)` 包了一层，加上了上下文信息。其中 `%w` 表示“包装”：它不仅把错误文本拼进来，还保留了原错误，形成一条错误链。如果改用 `%v`，就只会拼接文本，错误链就断了。
5. `errors.Is(err, ErrInvalidPort)` 沿着错误链逐层查找，判断其中是否有 `ErrInvalidPort`，结果是 `true`。
6. `errors.As(err, &input)` 沿着错误链查找 `*InputError` 类型的错误，找到后赋值给 `input`，于是可以读到 `input.Value`。这里要传 `&input`，是因为 `As` 需要修改 `input` 这个变量本身。

如果有多个相互独立的失败需要一起返回（比如批量校验表单），可以用 `errors.Join` 把它们合并成一个错误，`Is` 和 `As` 仍然能找到其中的每一个。

需要注意的是，用 `%w` 包装底层错误，意味着调用方可能会开始依赖它。如果以后更换底层实现，这些依赖就会失效。所以是否暴露底层错误，应该作为 API 设计的一部分认真考虑。

### 在哪里处理错误

一个错误从底层传到上层，通常会经过好几层函数。比较好的做法是：每一层只负责补充自己的上下文（像上例的 `read settings:`），然后继续返回；只有负责面向用户的那一层（比如 HTTP handler 或 CLI 的 `main`）才记录日志或展示错误。如果每一层都打印一次日志再返回，同一个错误就会出现在好几行日志里。

库函数遇到普通的失败时，也不应该调用 `log.Fatal` 或 `os.Exit`。它们会直接结束整个进程，调用方完全没有机会处理；而且 `os.Exit` 不会执行任何 defer，资源可能没有被正确清理。

### panic 和 recover

Go 也有类似异常的机制：`panic` 会中断当前函数，沿着调用栈逐层向上展开，直到程序崩溃。但它只用于程序本身出了 bug、无法继续运行的情况（比如数组越界、对 nil map 写入），不应该用来处理“文件不存在”这样可以预见的失败。

`recover` 可以拦截 panic，但限制很多：它只能在 defer 的函数里调用，而且只能拦截同一个 goroutine 里的 panic。另外，拦截了 panic 并不意味着状态恢复正常了：如果 panic 发生时数据已经改到一半，recover 不会帮你回滚。

## 8. 泛型与现代集合写法

### 类型参数

Go 1.18 加入了泛型，写法和 TypeScript 类似，只是把尖括号换成了方括号。下面的例子包含一个泛型栈、一个泛型排序函数和一个自定义迭代器：

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
	// 缩短 slice 不会清除底层数组；先释放这个位置保留的引用。
	s.items[i] = zero
	s.items = s.items[:i]
	return value, true
}

func ascending[T cmp.Ordered](values []T) []T {
	// Sort 会原地修改，因此先复制，保持调用方的数据不变。
	result := slices.Clone(values)
	slices.Sort(result)
	return result
}

func countdown(n int) iter.Seq[int] {
	return func(yield func(int) bool) {
		for ; n > 0; n-- {
			// 消费者 break 后 yield 返回 false，生产者必须立即停止。
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

`Stack[T any]` 定义了一个带类型参数 `T` 的结构体，`Stack[string]` 则是把 `T` 替换为 `string` 后得到的具体类型。

`Pop` 里有两处值得注意：

- 栈为空时要返回“没有值”，但 Go 没有 `undefined`，所以用 `var zero T` 得到 `T` 类型的零值返回，同时用第二个返回值 `false` 告诉调用方栈是空的。
- 弹出元素时，代码先把该位置设为零值，再缩短 slice。原因在第 4 节讲过：缩短 slice 只是改变了视图的长度，底层数组里的元素依然存在。如果元素是指针或包含指针，垃圾回收器会认为它仍被引用，无法回收。先清空这个位置，就能避免这种内存泄漏。

### 约束

方括号里 `T` 后面的部分叫做**约束**，它规定了 `T` 可以是哪些类型，也就决定了函数里能对 `T` 做什么操作：

- `any` 表示任意类型。因为什么类型都可能出现，函数里只能做对所有类型都合法的操作，比如赋值和传递，不能比较大小或相加。
- `comparable` 表示可以用 `==` 比较的类型，例如用作 map 键的类型。
- `cmp.Ordered` 表示可以用 `<` 比较大小的类型，包括整数、浮点数和字符串。`ascending` 需要排序，所以使用了这个约束。

也可以自定义约束。`~int` 表示“底层类型是 `int` 的所有类型”，所以它不仅包含 `int`，也包含 `type Count int` 这样的自定义类型；`~int | ~int64` 表示两者的并集。这种包含类型集合的接口只能用作约束，不能像普通接口那样用来声明变量。

泛型还有一个限制：方法不能声明自己的类型参数。`Stack[T]` 的方法可以使用 `T`，但不能再额外引入一个 `U`。遇到这种需求时，改写成泛型函数即可。

### 迭代器不是异步流

示例中的 `countdown` 返回一个 `iter.Seq[int]`，这是 Go 1.23 引入的迭代器。它的写法有点像 JS 的生成器函数，但实现原理不同。

`iter.Seq[T]` 实际上就是一个函数类型 `func(yield func(T) bool)`。当你写 `for n := range countdown(3)` 时，Go 会调用这个函数，并把循环体包装成 `yield` 传进去：迭代器每调用一次 `yield(n)`，循环体就执行一次。如果循环体执行了 `break`，`yield` 会返回 `false`，迭代器必须检查这个返回值并立即停止。这就是代码里 `if !yield(n) { return }` 的作用。

整个过程是普通的同步函数调用，没有启动 goroutine，也没有创建 channel，和 JS 的 async iterator 或 Node 的 stream 完全不同。

标准库很多地方都开始返回迭代器。例如 `maps.Keys` 返回 map 所有键的迭代器，`slices.Sorted` 把迭代器的内容收集到 slice 并排序。迭代器的好处是不必一次把所有数据放进内存，所以只有在确实需要完整列表时才收集成 slice。

迭代器能否重复使用，要看它的具体实现。本例的 `countdown` 在迭代过程中修改了捕获的 `n`，第一轮遍历结束后 `n` 已经变了，所以它只能用一次。

### 接口和泛型怎么选

接口和泛型都能让代码处理多种类型，但它们解决的是不同的问题：

- **接口**用于“不同类型都有某种行为，运行时可以替换”。例如函数接受 `io.Reader`，可以传文件，也可以传网络连接。
- **泛型**用于“保持输入和输出之间的类型关系”。例如 `ascending` 输入 `[]int` 就返回 `[]int`，输入 `[]string` 就返回 `[]string`，调用方不需要再做类型断言。

TypeScript 代码里泛型用得很多，但在 Go 里不必把每个函数都写成泛型。大多数业务代码直接使用具体类型就够了。

## 9. I/O、JSON 与标准库组合

### Reader 和 Writer

Go 标准库的 I/O 都围绕两个很小的接口构建：`io.Reader` 表示“可以从中读取字节”，`io.Writer` 表示“可以向其写入字节”。文件、网络连接、HTTP 响应体、内存缓冲区都实现了这些接口。因此，一个接受 `io.Reader` 的函数不用关心数据来自哪里，测试时也可以直接传入 `strings.NewReader("...")` 代替真实文件。

`Reader` 的 `Read` 方法有一个反直觉的规则：它可能在返回已读数据（`n > 0`）的**同时**返回一个错误（比如 `io.EOF`）。所以手写读取循环时，要先处理已经读到的 `n` 个字节，再检查错误，否则最后一段数据会丢失。

实际上，大多数时候不需要手写循环，`io.Copy`、`io.ReadAll`、`bufio` 和各种编码器都已经正确处理了这些细节。不过要注意，`io.ReadAll` 会把全部内容读进内存，对于外部输入，应当先限制最大读取量（后面的 HTTP 例子会演示）。

### 解析 JSON

下面的例子从一个 `io.Reader` 中解析配置：

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
	// 第一次 Decode 成功并不意味着输入已结束；拒绝尾部额外值。
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

在 JS 中，`JSON.parse` 直接返回一个任意结构的对象。Go 的做法是先定义结构体，再把 JSON 解码进去，这样字段和类型在编译期就是确定的。

结构体字段后面反引号里的 `` `json:"name"` `` 叫做**标签（tag）**，它告诉 JSON 库这个字段对应 JSON 中的哪个键。注意字段名必须大写开头（导出），否则 JSON 库无法访问，即使写了标签也没用。`Decode(&settings)` 传入的是地址，因为解码器需要往结构体里写数据。

这个函数刻意写得比较严格，做了三层检查：

1. `DisallowUnknownFields()` 拒绝结构体中没有定义的字段，这样配置里的拼写错误能被及时发现。
2. 第一次 `Decode` 成功后，再尝试解码一次。`Decoder` 是按流读取的，第一次成功只代表读到了一个完整的 JSON 值，并不代表输入结束了。如果第二次没有得到 `io.EOF`，说明后面还有多余内容，就报错。
3. 最后检查 `Retries` 不能为负数。JSON 格式正确，不代表数据在业务上是合理的，这一步检查不能省。

要不要拒绝未知字段、能否有多个 JSON 值、缺失字段用什么默认值，都取决于具体协议。本例选择了“严格的单对象配置文件”这种规则。

另外有一个精度问题：如果把 JSON 解码到 `any` 类型，传统的 `encoding/json` 会把所有数字都解析成 `float64`，超过 2^53 的大整数就会丢失精度（和 JS 的 `Number` 是同一个问题）。所以能确定结构时，应该优先解码到具体类型；确实需要处理动态结构时，可以用 `Decoder.UseNumber` 保留数字的原始文本。

本文使用的是传统的 `encoding/json`。新版 Go 中还能看到 `encoding/json/v2` 和 `encoding/json/jsontext`，它们的选项和部分默认行为（比如 nil 集合如何编码、字段名如何匹配）与旧版不同。阅读别人的代码时，要先看清导入的是哪个包。如果看到 `jsonv1 "encoding/json"` 这样的写法，那只是给导入的包起了个别名，方便在同一个文件里区分两套 API。

### 发起 HTTP 请求

用 Go 发 HTTP 请求和 JS 的 `fetch` 有一个相同的地方：服务器返回 404 或 500 时，请求本身不会报错。因此一个完整的请求需要分别处理三种失败：请求没有发出去（网络错误）、服务器返回了非成功的状态码、读取响应体时出错。

另外，`http.Client` 内部维护着连接池，可以在多个 goroutine 中并发使用。应该创建一个 Client 反复使用，而不是每次请求都新建一个，否则连接无法复用。

下面的程序用 `httptest` 在本机启动一个测试服务器，不需要访问互联网。程序里用到的 `context` 用于控制超时，第 11 节会详细讲解。

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
	// 获取成功后立即安排关闭；所有返回路径都释放响应体。
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	const maxBytes = 4096
	// 多读一个字节，才能区分“恰好达到上限”和“已超出上限”。
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
	// 提前成功也要释放计时器等资源，不必等到 deadline。
	defer cancel()
	text, err := fetchText(ctx, server.Client(), server.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(text)
}
```

`fetchText` 按顺序处理了前面说的三种失败：`client.Do` 返回错误说明请求没有成功发出；拿到响应后检查状态码；读取响应体时再检查错误。

拿到 `resp` 后，代码立刻写了 `defer resp.Body.Close()`。响应体占用着一个网络连接，不关闭的话连接就无法回到连接池。这正是第 5 节说的“获取成功后立刻注册清理”。

读取响应体时，用 `io.LimitReader` 限制了最大读取量，防止对方返回一个巨大的响应把内存耗尽。上限设为 `maxBytes + 1` 是一个小技巧：如果正好读到了 `maxBytes + 1` 个字节，就说明响应超出了限制；如果只限制为 `maxBytes`，就无法区分“响应恰好这么大”和“响应被截断了”。

关于连接复用还有一个细节：响应体被完整读到末尾再关闭，连接才能被复用。成功路径会读到末尾，没有问题；状态码不对时，示例直接关闭了响应体，这个连接就无法复用。实际项目中可以视情况读取一部分错误响应再关闭，但要设上限，不能为了复用连接而无限读取。

### 文件路径和外部进程

Go 有两个处理路径的包，用途不同：`path/filepath` 处理操作系统的文件路径，会根据平台使用 `/` 或 `\`；`path` 只处理用 `/` 分隔的逻辑路径，比如 URL 路径。

需要注意的是，`filepath.Join` 和 `filepath.Clean` 只负责拼接和规范化路径，不提供安全保护。如果用户输入了 `../../etc/passwd`，拼接后的路径照样会跳出预期目录。处理用户提供的路径时，必须自己限制访问范围。新版 Go 提供了 `os.Root`，可以把文件操作限制在指定目录内，使用前请阅读它的文档，了解具体的保护范围。

启动外部进程时，应该用 `exec.CommandContext(ctx, program, arg1, arg2)` 把参数逐个传入，而不是把用户输入拼成字符串交给 `sh -c` 执行，否则就会出现命令注入漏洞。另外，`CommandContext` 在 context 取消时只会结束它直接启动的那个进程，不会自动清理该进程派生出的子进程。如果需要可靠地清理整个进程树，就要另外处理进程组、管道关闭等问题，而且不同平台的行为也不一样。

## 10. 并发与同步

### goroutine 不是 Promise

在 Go 里，`go f()` 会启动一个 goroutine，让 `f` 和当前代码并发执行。goroutine 可以理解为由 Go runtime 管理的轻量级线程。

JS 开发者容易把它类比成 Promise，但两者差别很大。`go f()` 不返回任何东西，你没有办法 `await` 它，也不会自动知道它什么时候结束、是否出错。如果需要拿到结果，就必须自己用 channel 或 `WaitGroup` 等工具来同步，下面会逐一介绍。

另一个更重要的区别是：JS 只有一个主线程，在两个 `await` 之间的代码不会被其他 JS 代码打断，所以访问共享变量是安全的。Go 不是这样：调度器会把 goroutine 分配到多个操作系统线程上，它们可能真的在同一时刻运行。因此，只要有多个 goroutine 访问同一份数据，并且其中有写操作，就必须进行同步。

这里顺便区分两个概念：**并发**是指多个任务在时间上交错推进，**并行**是指多个任务在同一时刻真正同时执行。在多核机器上，goroutine 既是并发的，也可能是并行的。

goroutine 的开销很小，启动成千上万个也没问题。但它们使用的资源并不会因此变得无限：网络连接、文件描述符、内存、对方服务器的限流都有上限，所以实际任务中仍然需要控制同时运行的数量。

### channel：在 goroutine 之间传递数据

channel 是 goroutine 之间传递数据的管道。一个 goroutine 往里发送，另一个 goroutine 从里面接收。下面的例子实现了一个常见的模式：多个 worker 从任务 channel 取任务，计算后把结果发到结果 channel。

```go
// tutorial: channels
package main

import (
	"fmt"
	"sync"
)

func main() {
	// 无缓冲 channel 的发送要等待接收方，天然限制在途数据。
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
		// 唯一的 jobs 发送方负责关闭；函数结束后 worker 的 range 才退出。
		defer close(jobs)
		for value := 1; value <= 4; value++ {
			jobs <- value
		}
	}()

	go func() {
		// 必须确认所有 worker 都已停止发送，才能关闭 results。
		workers.Wait()
		close(results)
	}()

	total := 0
	// 接收与 worker 发送并行进行；先阻塞等待 worker 会造成死锁。
	for result := range results {
		total += result
	}
	fmt.Println(total)
}
```

程序输出 `30`（1 + 4 + 9 + 16）。由于两个 worker 并发执行，各个结果到达的顺序是不确定的，但总和不变。

这个程序有三个角色，理解它们的配合关系是理解 channel 的关键：

1. **两个 worker**：用 `for value := range jobs` 不断从 `jobs` 取任务，直到 `jobs` 被关闭为止，然后把计算结果发到 `results`。
2. **生产者 goroutine**：把 1 到 4 发进 `jobs`，发完后关闭 `jobs`。关闭 channel 是在通知 worker：“不会再有新任务了”，worker 的 `range` 循环因此结束。
3. **关闭 results 的 goroutine**：等所有 worker 都结束后，关闭 `results`。这样 `main` 中的 `for result := range results` 才能结束。

为什么要单独开一个 goroutine 去等待 worker？因为 `results` 是无缓冲的：worker 每发送一个结果，都要等 `main` 接收后才能继续。如果 `main` 先调用 `workers.Wait()` 等 worker 结束，worker 卡在发送上等 `main` 接收，`main` 又在等 worker 结束，双方互相等待，就形成了死锁。所以必须让接收和等待同时进行。

`sync.WaitGroup` 用来等待一组 goroutine 结束。`workers.Go(f)` 会启动一个 goroutine 并记录下来，`workers.Wait()` 会阻塞直到所有记录的 goroutine 都返回。在较早的代码里，你会看到另一种写法：先调用 `wg.Add(1)`，再用 `go` 启动 goroutine，并在其中 `defer wg.Done()`。用这种写法时，`Add` 必须在 `go` 语句之前调用；否则 `Wait` 可能在 goroutine 还没来得及 `Add` 时就看到计数为零，提前返回了。

### channel 的规则

**无缓冲与有缓冲。** `make(chan int)` 创建的是无缓冲 channel，发送方必须等到接收方取走数据才能继续，相当于一次面对面的交接。`make(chan int, 10)` 创建的是容量为 10 的有缓冲 channel，缓冲区没满时，发送方可以不等接收方直接继续。

channel 不只是一个线程安全的队列。一次成功的发送和接收还保证了：发送方在发送之前写入的数据，接收方在接收之后一定能看到。这就是 Go 内存模型中的同步关系，也是 channel 能安全地在 goroutine 之间传递数据的原因。

**方向限制。** 函数参数可以声明为 `chan<- T`（只能发送）或 `<-chan T`（只能接收）。这样，编译器就能阻止函数做出越权的操作，比如 worker 不小心关闭了任务 channel。

**谁来关闭。** 关闭 channel 的作用是告诉接收方“不会再有数据了”，所以只有能确定“所有发送都已结束”的一方才能关闭它。只有一个发送方时，由它自己关闭即可，就像例子中的生产者。有多个发送方时，任何一个发送方都不能确定其他发送方是否结束，所以不能让每个发送方都 `defer close(ch)`；应该像例子中关闭 `results` 那样，由一个专门的协调者在等所有发送方结束后再关闭。

各种边界情况的行为如下：

| 操作 | 行为 |
| --- | --- |
| 从已关闭且已取空的 channel 接收 | 立即返回零值；用 `v, ok := <-ch` 接收时，`ok` 为 false |
| `for value := range ch` | 持续接收，直到 channel 被关闭且数据被取空 |
| 向已关闭的 channel 发送，或重复关闭 | panic |
| 向 nil channel 发送或从中接收 | 永远阻塞 |
| 关闭 nil channel | panic |

**select。** `select` 可以同时等待多个 channel 操作，哪个先就绪就执行哪个分支。如果同时有多个分支就绪，Go 会随机选择一个，而不是按代码中的书写顺序。如果加了 `default` 分支，那么在所有操作都无法立即执行时会直接走 `default`，不会阻塞。但要小心：在循环里使用带 `default` 的 `select`，如果没有其他阻塞手段，循环就会一直空转，占满 CPU。

### 用锁保护共享状态

channel 适合在 goroutine 之间传递数据。如果多个 goroutine 只是需要读写同一份状态，比如一个计数器或缓存，用锁通常更直接：

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
	// 锁在本次方法返回时释放；不会一直持有到调用方的循环结束。
	defer c.mu.Unlock()
	c.n++
}

func (c *Counter) Value() int {
	// 读取也使用同一把锁；指针接收者避免复制已使用的 Mutex。
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

4 个 goroutine 各加 100 次，输出 `400`。如果去掉锁，`c.n++` 这个“读取、加一、写回”的过程可能被其他 goroutine 打断，最终结果就会小于 400。

这个例子有几点值得注意：

**读操作也要加锁。** 很多人以为只有写操作需要加锁，读操作直接读字段就行，这是错误的。如果读的同时有人在写，读到的可能是不完整的数据，而且 Go 内存模型不保证没有同步的读能看到其他 goroutine 的写入。所以，所有访问受保护数据的地方都必须遵守同一套加锁规则。

**锁保护的是不变量。** 加锁时考虑的不应该是“哪一行代码看起来危险”，而是“哪些数据必须保持一致”。例如余额和交易记录必须同时更新，那么这两个字段就要由同一把锁保护，并且在同一次加锁期间一起修改。

**同步原语不能复制。** `Mutex`、`RWMutex`、`WaitGroup`、`Once` 这些类型在使用后都不能复制。复制会得到一个独立的锁，原来的保护就失效了。这就是 `Counter` 的方法都使用指针接收者的原因（`go vet` 可以检查出这类复制）。

**不要把受保护的数据直接返回出去。** 如果 `Value` 返回的不是一个整数，而是锁所保护的那个 map，调用方拿到后就能在锁外随意修改它，锁也就形同虚设了。应该返回一份副本，或者只提供受锁保护的操作方法。

其他同步工具简要介绍如下：

- `sync.RWMutex` 允许多个读操作同时进行，写操作时独占。但它的开销比普通 `Mutex` 大，并不总是更快，应该根据实际读写比例和测量结果选择。
- `sync.Once` 保证某个操作只执行一次，常用于延迟初始化。要注意，即使这个操作失败了，它也不会再执行第二次，所以不适合用于“失败了要重试”的初始化。
- `sync/atomic` 提供原子操作，适合单个独立的计数器或标志位。但多个原子变量组合在一起时，并不能保证它们之间的一致性。如果几个字段之间有关联，用一把锁会简单可靠得多。

### 用 race 检测器检查数据竞争

`go test -race` 和 `go run -race` 会启用数据竞争检测器，在程序运行时报告没有正确同步的并发访问。它非常有用，但只能发现实际执行到的代码路径中的问题。一次运行没有报错，不能证明程序就没有数据竞争，最终还是要靠理清哪些数据被共享、由什么来同步。

## 11. 取消、后台任务与接口契约

### context：控制操作的生命周期

前面的例子里，后台任务都会自己执行完毕。但在实际应用中，经常需要提前停止一个操作：用户关闭了页面、请求超时了、程序正在退出。JS 用 `AbortController` 和 `AbortSignal` 解决这个问题，Go 对应的工具是 `context.Context`。

context 用来传递取消信号、截止时间（deadline），以及少量与当前请求相关的数据。惯例是：凡是可能阻塞或需要支持取消的函数，都把 `ctx context.Context` 作为第一个参数。不要传 nil；如果没有上层传入的 context，就用 `context.Background()` 作为起点。

创建可取消的 context 有三个函数：`WithCancel`、`WithTimeout`、`WithDeadline`。它们都会基于一个父 context 派生出新的 context，同时返回一个 `cancel` 函数。调用 `cancel` 会取消这个 context 以及从它派生的所有 context。即使操作已经正常完成，也应该调用 `cancel` 释放内部资源（比如计时器），所以通常紧接着写 `defer cancel()`。`cancel` 重复调用也没有问题。

被调用的函数如何得知自己被取消了？`ctx.Done()` 返回一个 channel，context 被取消时这个 channel 会被关闭，所以可以在 `select` 中监听它。`ctx.Err()` 返回取消的原因：`context.Canceled` 表示被主动取消，`context.DeadlineExceeded` 表示超时。如果需要传递更具体的取消原因，可以使用 `WithCancelCause` 和 `context.Cause`。

最关键的一点是：**Go 的取消是协作式的**。取消 context 不会强行终止任何 goroutine，它只是发出一个信号，被调用的代码需要自己检查并主动退出。标准库的网络操作会响应 context；一段纯 CPU 计算的循环需要你在循环里主动检查 `ctx.Err()`；而一个普通的 `io.Reader` 能否被中断，则取决于它的具体实现。

### 区分最终结果、取消和进度

一个长时间运行的任务，通常要向调用方报告三种信息：进度（做到了哪一步）、最终结果（成功了还是失败了）、以及被取消时已经完成了哪些部分。下面的例子演示了如何把这三者区分开。

`Process` 是一个同步函数，它不自己启动 goroutine，是否放到后台运行由调用方决定。它通过回调函数报告进度，通过返回值给出最终结果；如果中途被取消，它会返回已经处理完的那部分结果。

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
		// 在处理下一项前协作检查取消，返回已完成部分和取消原因。
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Values = append(result.Values, strings.TrimSpace(value))
		if report != nil {
			// 这是同步回调；它的阻塞会传递给当前任务。
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
	// 只有一个最终结果；容量 1 允许后台先完成，不等接收方调度。
	done := make(chan outcome, 1)
	go func() {
		result, err := Process(ctx, []string{" one ", " two ", " three "}, func(p Progress) {
			fmt.Printf("%d/%d\n", p.Done, p.Total)
			if p.Done == 2 {
				// cancel 发出信号，不会在这里强制终止 Process。
				cancel()
			}
		})
		done <- outcome{result: result, err: err}
	}()

	// 接收最终结果，既等待任务退出，也取得不可丢失的错误信息。
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

执行过程是这样的：`main` 在后台 goroutine 中运行 `Process`。处理完第 2 项后，进度回调调用了 `cancel()`。但 `cancel` 只是发出信号，并没有立即中断 `Process`，回调正常返回。直到 `Process` 准备处理第 3 项时，检查 `ctx.Err()` 发现已被取消，于是返回了前两项结果和 `context.Canceled` 错误。

`main` 没有在调用 `cancel` 后直接退出，而是通过 `<-done` 等待后台任务返回最终结果。这一步有两个作用：一是确保后台 goroutine 已经真正结束，没有遗留下来；二是拿到最终的错误信息，不会丢失。启动 goroutine 的代码应该始终清楚三件事：它什么时候退出，错误如何报告，如果调用方提前离开了，它会不会卡住。

`done` 的容量设为 1，正是为了处理最后一种情况：即使 `main` 已经不再接收，后台 goroutine 也能把结果放进缓冲区然后退出，不会永远阻塞在发送上。

### 设计进度报告时要考虑的问题

本例的进度回调是同步调用的，回调执行得慢，任务也会跟着变慢。如果任务由多个 worker 并发处理，进度回调的设计还要考虑更多问题：回调会不会被并发调用？进度事件是否保证有序？回调能不能阻塞？调用方不再关心进度之后怎么办？

另外，进度和最终结果的重要性不同。进度事件丢掉一些问题不大，比如下载进度可以合并成最新的一条；但成功或失败的最终结果绝对不能丢，所以不应该只通过进度事件来传达，而应该像本例一样通过返回值传递。

如果用一个有缓冲的队列来传递进度事件，可以限制积压占用的内存，但队列满了之后是等待、合并还是丢弃，也需要事先明确规定。

还有一条很重要的规则：**不要在持有锁的时候调用外部传入的回调**。你无法控制回调里做了什么。如果它反过来调用了你组件的某个方法，而那个方法也要获取同一把锁，或者它在等待另一个正在等这把锁的 goroutine，程序就会死锁。

### 把核心逻辑和呈现分开

上面的 `Process` 只接收输入、返回结果和错误，它自己不打印日志、不弹窗、也不退出进程。这是一种值得遵循的设计：可复用的核心代码只负责处理数据并保证数据一致性；打印输出、处理信号、与用户交互，都交给调用方完成。这样同一份核心代码可以同时用于命令行、服务端和测试。

最后，有两个常见的误解需要澄清：

- **context 不是事务。** 取消只是停止后续操作，不会撤销已经完成的修改。如果一个任务正在依次修改多个文件，被取消时可能已经改了一半。所以要么只在安全的时间点检查取消，要么在取消后执行回滚或补完。另外，对单个文件做原子 rename 可以保证这一个文件的替换是原子的，但多个文件组成的整体操作并不会因此自动变成原子的。
- **Mutex 只在进程内有效。** `sync.Mutex` 只能协调同一个进程里的 goroutine。如果多个独立进程（比如同一个命令行工具同时运行了两次）要操作同一份数据，就需要用文件锁等操作系统级别的机制。即使这些进程使用的是同一个 Go 包，也不会改变这一点。

## 12. 阅读与验证真实仓库

### 从入口和边界开始

拿到一个陌生的 Go 仓库，建议按以下顺序阅读：

1. **先读 `go.mod`**，了解模块路径、要求的 Go 版本和直接依赖。
2. **再找入口**，也就是 `cmd/` 目录或 `package main` 所在的位置，看程序启动时创建了哪些对象、它们是怎么连接起来的。
3. **然后选一个具体功能，沿着调用链往下读**，弄清楚它的输入是什么、经过了哪些函数、产生了哪些副作用（写文件、发请求）、最终返回什么。

不需要一开始就逐行读完底层实现。遇到不熟悉的类型时，先看它的公开方法、注释、调用它的代码和它的测试，这些通常足以说明它的用途和使用规则。

Go 不允许包之间循环依赖。如果你在修改代码时遇到循环依赖的编译错误，通常说明包的划分需要调整，比如把共用的部分抽到一个新包里，而不是加一个全局注册表绕过去。

下面这些只读命令可以帮助你快速了解仓库：

```sh
go list ./...
go doc -all ./某个包目录
go list -deps ./某个入口目录
go env GOOS GOARCH GOVERSION
```

依次是：列出所有包、查看某个包的完整文档、列出某个入口依赖的所有包、查看当前的目标平台和 Go 版本。其中“某个包目录”要替换为仓库中的实际路径。

这些命令和编辑器的“跳转到定义”功能可以配合使用。读代码时，带着这几个问题去看效率会更高：这个值是什么类型？谁创建了它？谁负责关闭它？谁会修改它？这比逐个追踪每个函数的实现要快得多。

### 平台文件、初始化与生成代码

在真实仓库中，你会遇到一些在入门示例中见不到的特殊写法：

| 形式 | 含义 |
| --- | --- |
| `file_darwin.go`、`file_linux.go`、`file_arm64.go` | 文件名以操作系统或 CPU 架构结尾，该文件只在对应平台上编译 |
| `//go:build ...` | 写在文件开头的构建约束，用表达式更灵活地控制文件是否参与编译 |
| `init()` | 包初始化函数，在 `main` 之前自动运行；被依赖的包先初始化 |
| `_ "some/package"` | 只为了执行该包的 `init` 而导入，本文件不会使用它的任何名字 |
| `//go:embed` | 在编译时把文件内容嵌入程序；这和运行时从磁盘读文件是两回事 |
| `//go:generate` | 声明一条代码生成命令，需要手动运行 `go generate` 才会执行，`go build` 不会自动运行它 |
| `go.mod` 中的 `tool` 依赖 | 让开发工具的版本也由 module 管理，通常用 `go tool 工具名` 调用 |
| `Code generated ... DO NOT EDIT.` | 自动生成的文件。要修改它，应该修改生成它的源文件或生成器，然后重新生成 |

关于 `init`：它适合做一些必须进行的、局部的初始化，比如预编译一个正则表达式。但不要在 `init` 里发网络请求、启动无法停止的后台任务，或者修改全局状态。这些操作会在导入包的时候自动发生，调用方无法控制，也很难测试。有些 GUI 框架要求在 `init` 中做特殊处理（例如把主 goroutine 绑定到主线程），那是平台本身的限制，不应该当作普通业务代码的写法。

### 反射

有些库需要在运行时检查任意值的类型和结构，例如 JSON 库要知道结构体有哪些字段、每个字段的标签是什么。这就要用到反射，也就是 `reflect` 包。

反射中有三个基本概念：`reflect.Type` 描述一个类型；`reflect.Value` 包装一个具体的值；`Kind` 表示值的大致类别，比如 struct、slice、指针。注意 `Kind` 只是粗略分类，两个 `Kind` 都是 struct 的值，可能属于完全不同的类型。

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
	// 从地址进入再解引用，才能获得可修改原变量的反射值。
	value := reflect.ValueOf(&record).Elem()
	age := value.FieldByName("Age")
	if age.IsValid() && age.CanSet() && age.Kind() == reflect.Int {
		age.SetInt(37)
	}
	fmt.Println(record.Age)
}
```

程序前半部分遍历 `Record` 的字段，打印每个字段的名字和 `json` 标签，这正是 JSON 库在内部做的事情。

后半部分通过反射修改字段。这里有一个容易出错的地方：为什么要写 `reflect.ValueOf(&record).Elem()`，而不是直接 `reflect.ValueOf(record)`？因为 Go 传参会复制值（第 3 节），`ValueOf(record)` 拿到的只是一份副本，修改它没有意义，反射库也不允许修改。传入指针，再用 `Elem()` 取得指针指向的值，得到的才是原变量本身，可以修改。

修改前，代码做了一连串检查：字段是否存在（`IsValid`）、是否可以修改（`CanSet`，例如未导出的字段就不能修改）、类型是否正确（`Kind`）。反射跳过了编译期的类型检查，这些检查都要自己完成，否则就会在运行时 panic。

反射主要是给框架和库使用的，例如序列化库、依赖注入或代码生成工具。在普通业务代码中，类型通常是已知的，直接调用方法更简单、更安全，也更快。

### 编写测试

Go 内置的 `testing` 包已经能满足大部分测试需求，不需要再引入像 Jest 那样的测试框架。规则很简单：测试文件以 `_test.go` 结尾，测试函数的签名是 `func TestXxx(t *testing.T)`。

测试文件有两种包声明方式：和被测代码使用同一个包名，就可以访问未导出的函数和字段；使用 `包名_test` 作为包名，则只能访问公开的 API，这样的测试更接近真实调用方的使用方式。

下面是一个完整的测试文件，保存为 `words_test.go`。为了让它能单独运行，被测试的两个函数也写在了同一个文件里；在真实项目中，它们应该放在普通的 `.go` 文件中。

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
	// 每个测试拥有隔离目录，测试结束后由 testing 自动清理。
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

`TestCount` 使用了 Go 中最常见的**表格驱动测试**写法：先把所有测试用例列成一个表（一个结构体 slice），然后在循环中逐个运行。每个用例通过 `t.Run` 作为一个有名字的子测试运行，失败时能清楚地看到是哪个用例出了问题。

`TestCountFile` 需要读写真实文件。`t.TempDir()` 会为这个测试创建一个独立的临时目录，测试结束后自动删除，不会影响其他测试或留下垃圾文件。如果有其他资源需要在测试结束时清理，可以用 `t.Cleanup` 注册清理函数。这个测试还验证了错误处理：读取不存在的文件时，返回的错误经过 `%w` 包装后，仍然能被 `errors.Is` 识别为 `fs.ErrNotExist`。

同理，测试 HTTP 客户端时，可以像第 9 节那样用 `httptest` 启动一个本地服务器，避免测试依赖外部网络。

测试应该优先覆盖最容易出问题的地方：输入的边界情况、错误是否正确传递、资源是否被释放、持久化的数据是否一致、并发时是否正确。而简单的字段读写、只是转发调用的方法、标准库本身的功能，就没有必要专门测试。测试的名字和断言应该描述外部可观察的行为，而不是锁定内部函数的调用顺序，否则每次重构都要修改测试。

如果需要做性能测试，先明确测试条件（数据量多大、缓存是冷的还是热的），再用 benchmark 或 profile 找出耗时的地方。新版 Go 的 benchmark 函数 `BenchmarkXxx` 中可以使用 `b.Loop()` 编写循环。注意不要把一次网络下载的耗时当成稳定的 CPU 性能数据，也不要仅仅为了提高覆盖率而编写重复的测试。

### 底层代码：主线程、unsafe 与 FFI

大部分应用代码只需要用到前面介绍的类型、接口和并发机制。但如果你阅读的是桌面 GUI 框架或调用系统 API 的库，就会遇到一些更底层的内容。下面这些知识点可以帮助你看懂这类代码在做什么：

- **`runtime.LockOSThread`**：把当前 goroutine 固定在当前的操作系统线程上。正常情况下 goroutine 会在线程之间切换，但有些 UI 系统（如 macOS 的 Cocoa）要求事件循环必须在主线程上运行，所以框架需要在初始化时用它来锁定主线程。
- **`unsafe.Pointer`**：允许绕过类型系统直接操作内存地址。需要特别注意的是，`uintptr` 只是一个整数，即使它的值等于某个对象的地址，垃圾回收器也不会因此保留这个对象。`unsafe` 的转换必须严格遵守官方文档列出的几种合法模式。
- **FFI（外部函数接口）**：用于调用 C 库或系统 API，涉及动态库加载、符号查找、调用约定和回调函数等问题。即使代码中没有使用 cgo，框架也可能通过其他方式调用原生库。
- **原生对象的内存管理**：Go 的垃圾回收器只管理 Go 自己分配的内存。原生对象可能需要手动增减引用计数（retain/release），传给原生代码的回调需要保证一直有效，有些对象只能在特定线程上访问。`defer` 可以帮助组织清理代码，但具体该怎么清理，要看原生 API 的文档。
- **更底层的实现**：修改可执行文件、图形后端、汇编跳板代码（trampoline）属于专门的实现层。如果你只是阅读上层业务代码或公开 API，只需要了解这一层的输入输出、线程要求和对象生命周期，不需要深入其实现细节。

以 MyGo 这类 Go 原生 UI 框架为例，建议先从公开类型、组件生命周期、事件回调和视图函数入手，再根据需要深入平台相关的实现。它的界面代码虽然看起来和 React 有些相似，但其中的方法链、泛型事件和闭包仍然遵循本文介绍的 Go 规则。还有一条通用原则：耗时的任务不要在界面事件线程上执行，否则界面会卡住；后台任务完成后，要通过框架提供的方法切换回界面线程，再更新界面状态。

## 参考资料

- [Go 语言规范](https://go.dev/ref/spec)：类型、方法集、接口、赋值和控制流的权威定义。
- [Go modules 参考](https://go.dev/ref/mod)：module、版本选择、`go.mod` 与工具链。
- [Go 内存模型](https://go.dev/ref/mem)：goroutine 之间的可见性与同步保证。
- [标准库文档](https://pkg.go.dev/std)：各个接口的契约、错误语义和示例，应以具体包的文档为准。
- [context](https://pkg.go.dev/context)、[sync](https://pkg.go.dev/sync)、[iter](https://pkg.go.dev/iter)：取消、同步和迭代器。
- [unsafe](https://pkg.go.dev/unsafe)：指针转换的合法模式与限制。
- [MyGo 架构文档](https://github.com/egoist/mygo/blob/v0.6.4/docs/architecture.md)：可以作为阅读真实 Go 框架的练习材料，阅读时注意它的版本和平台限制。
