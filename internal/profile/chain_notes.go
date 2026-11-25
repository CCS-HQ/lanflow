package profile

import "fmt"

// ChainNote is something chain composition or validation says: Code
// names its text in chainFormats, Args fill it (a ChainNote among them is
// a nested text, the reason a chain was left out). It is also the error
// NormalizeProxyChain returns. Error is the Chinese text; the interface
// translates Format with i18n (app/chains.go).
type ChainNote struct {
	Code string
	Args []any
}

// Format is the note's text, with fmt verbs for Args.
func (n ChainNote) Format() string { return chainFormats[n.Code] }

func (n ChainNote) Error() string {
	args := make([]any, len(n.Args))
	for i, a := range n.Args {
		if sub, ok := a.(ChainNote); ok {
			a = sub.Error()
		}
		args[i] = a
	}
	return fmt.Sprintf(n.Format(), args...)
}

func chainNote(code string, args ...any) ChainNote {
	if _, ok := chainFormats[code]; !ok {
		panic("profile: unknown chain note " + code)
	}
	return ChainNote{Code: code, Args: args}
}

func chainNoteP(code string, args ...any) *ChainNote {
	n := chainNote(code, args...)
	return &n
}

// chainFormats are the texts of the chain notes. They are i18n keys too:
// the catalog test reads this table (internal/i18n keyTables).
var chainFormats = map[string]string{
	"empty-name":       "链路名称不能为空",
	"reserved-name":    "链路名称不能以 %s 开头（澜流保留）",
	"hop-count":        "链路需要 %d–%d 跳，现在是 %d 跳",
	"hop-empty":        "第 %d 跳没有选择节点",
	"hop-reserved":     "第 %d 跳「%s」是澜流生成的出站，不能放进链路",
	"hop-twice":        "「%s」在链路里出现了两次",
	"hop-self":         "链路不能把自己作为一跳",
	"loop-chain":       "入口组「%s」包含链路组「%s」，会形成环路",
	"loop-container":   "入口组「%s」包含「%s」组，会形成环路",
	"loop-first":       "入口组「%s」包含「%s」组，而「%s」会放进这个组，会形成环路",
	"entry-chain":      "入口「%s」是另一条链路，链路不能嵌套",
	"entry-missing":    "入口「%s」不在当前配置的节点或代理组里（订阅提供方的节点需先放进一个代理组）",
	"hop-group":        "第 %d 跳「%s」是代理组，中间与落地只能是节点",
	"hop-missing":      "第 %d 跳「%s」不在当前配置的 proxies 里（可能已随订阅更新删除；订阅提供方的节点不能做中间或落地）",
	"wireguard":        "链路「%s」：第 %d 跳「%s」是 WireGuard 节点，与原节点共用私钥，同时使用会互相顶掉会话",
	"own-dialer":       "链路「%s」：第 %d 跳「%s」自带 dialer-proxy「%s」，在链路中被改为前一跳",
	"container-taken":  "订阅中已有不是 select 的「%s」和「%s」组，链路不会生成",
	"ignored-no-name":  "有一条链路没有名称，已忽略",
	"ignored-reserved": "链路名称「%s」以 %s 开头（澜流保留），已忽略",
	"ignored-dup":      "链路名称「%s」重复，后一条已忽略",
	"ignored-builtin":  "链路名称「%s」与内置策略或链路总组重名，已忽略",
	"ignored-node":     "链路名称「%s」与配置中的节点重名，已忽略",
	"ignored-group":    "链路名称「%s」与配置中的代理组重名，已忽略",
	"disabled-because": "链路「%s」已停用：%s",
	"placeholder":      "链路「%s」已停用，命中它的流量被拒绝",
}
