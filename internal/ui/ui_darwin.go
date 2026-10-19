//go:build darwin

package ui

import (
	"errors"
	"runtime"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func init() {
	// Cocoa must run on the main thread.
	runtime.LockOSThread()
}

type nsPoint struct{ X, Y float64 }
type nsSize struct{ W, H float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}

var sel = objc.RegisterName

func cls(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func nsStr(s string) objc.ID { return cls("NSString").Send(sel("stringWithUTF8String:"), s) }

func menuItem(title, action, key string) objc.ID {
	it := cls("NSMenuItem").Send(sel("alloc"))
	return it.Send(sel("initWithTitle:action:keyEquivalent:"), nsStr(title), sel(action), nsStr(key))
}

func submenu(parent objc.ID, title string, items ...objc.ID) {
	top := cls("NSMenuItem").Send(sel("new"))
	m := cls("NSMenu").Send(sel("alloc")).Send(sel("initWithTitle:"), nsStr(title))
	for _, it := range items {
		m.Send(sel("addItem:"), it)
	}
	top.Send(sel("setSubmenu:"), m)
	parent.Send(sel("addItem:"), top)
}

func sep() objc.ID { return cls("NSMenuItem").Send(sel("separatorItem")) }

func Run(o Options) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("не удалось открыть окно")
		}
	}()
	for _, fw := range []string{"Cocoa", "WebKit"} {
		if _, err := purego.Dlopen("/System/Library/Frameworks/"+fw+".framework/"+fw, purego.RTLD_GLOBAL|purego.RTLD_LAZY); err != nil {
			return err
		}
	}
	app := cls("NSApplication").Send(sel("sharedApplication"))
	app.Send(sel("setActivationPolicy:"), 0)

	delegateClass, err := objc.RegisterClass("UzbekAppDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{
		Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"),
		Fn:  func(self objc.ID, cmd objc.SEL, sender objc.ID) bool { return true },
	}})
	if err != nil {
		return err
	}
	app.Send(sel("setDelegate:"), objc.ID(delegateClass).Send(sel("new")))

	// Main menu: without the Edit menu ⌘C/⌘V/⌘A don't work in text fields.
	mainMenu := cls("NSMenu").Send(sel("new"))
	submenu(mainMenu, o.Title,
		menuItem("Скрыть "+o.Title, "hide:", "h"), sep(), menuItem("Выйти из "+o.Title, "terminate:", "q"))
	submenu(mainMenu, "Правка",
		menuItem("Отменить", "undo:", "z"), menuItem("Повторить", "redo:", "Z"), sep(),
		menuItem("Вырезать", "cut:", "x"), menuItem("Копировать", "copy:", "c"),
		menuItem("Вставить", "paste:", "v"), menuItem("Выделить всё", "selectAll:", "a"))
	submenu(mainMenu, "Окно",
		menuItem("Свернуть", "performMiniaturize:", "m"), menuItem("Закрыть", "performClose:", "w"))
	app.Send(sel("setMainMenu:"), mainMenu)

	rect := nsRect{Size: nsSize{float64(o.Width), float64(o.Height)}}
	const style = 1 | 2 | 4 | 8 // titled | closable | miniaturizable | resizable
	win := cls("NSWindow").Send(sel("alloc")).Send(sel("initWithContentRect:styleMask:backing:defer:"), rect, style, 2, false)
	win.Send(sel("setTitle:"), nsStr(o.Title))
	win.Send(sel("setMinSize:"), nsSize{900, 600})
	win.Send(sel("setReleasedWhenClosed:"), false)
	if dark := cls("NSAppearance").Send(sel("appearanceNamed:"), nsStr("NSAppearanceNameDarkAqua")); dark != 0 {
		win.Send(sel("setAppearance:"), dark)
	}
	win.Send(sel("setBackgroundColor:"), cls("NSColor").Send(sel("colorWithRed:green:blue:alpha:"), 0.078, 0.082, 0.094, 1.0))
	win.Send(sel("center"))
	win.Send(sel("setFrameAutosaveName:"), nsStr("UzbekMainWindow"))

	conf := cls("WKWebViewConfiguration").Send(sel("new"))
	wv := cls("WKWebView").Send(sel("alloc")).Send(sel("initWithFrame:configuration:"), rect, conf)
	wv.Send(sel("setValue:forKey:"), cls("NSNumber").Send(sel("numberWithBool:"), false), nsStr("drawsBackground"))
	wv.Send(sel("setAutoresizingMask:"), 2|16)
	win.Send(sel("setContentView:"), wv)
	u := cls("NSURL").Send(sel("URLWithString:"), nsStr(o.URL))
	wv.Send(sel("loadRequest:"), cls("NSURLRequest").Send(sel("requestWithURL:"), u))

	win.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	app.Send(sel("activateIgnoringOtherApps:"), true)

	focus = func() {
		app.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("unhide:"), objc.ID(0), false)
	}
	if o.Quit != nil {
		go func() {
			<-o.Quit
			app.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("terminate:"), objc.ID(0), false)
		}()
	}
	app.Send(sel("run"))
	return nil
}
