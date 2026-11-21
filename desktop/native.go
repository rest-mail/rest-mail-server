package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"

	"github.com/rest-mail/rest-mail-server/desktop/internal/attachments"
	"github.com/rest-mail/rest-mail-server/desktop/internal/native"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/dock"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

type composeTab struct {
	Dirty     bool
	CanSave   bool
	Busy      bool
	Prompting bool
}

type ComposeStatus struct {
	Dirty   bool   `json:"dirty"`
	CanSave bool   `json:"canSave"`
	Busy    bool   `json:"busy"`
	Subject string `json:"subject"`
}

type Native struct {
	app           *application.App
	dock          *dock.DockService
	notifications *notifications.NotificationService
	mutex         sync.Mutex
	states        map[string]native.State
	compose       map[string]*composeTab
	closed        map[string]bool
	items         map[string]*application.MenuItem
	settings      *application.MenuItem
	quitting      bool
	notified      map[int]bool
	files         attachments.Holder
	dropSequence  uint64
}

func newNative() *Native {
	return &Native{dock: dock.New(), notifications: notifications.New(), states: make(map[string]native.State), compose: make(map[string]*composeTab), closed: make(map[string]bool), items: make(map[string]*application.MenuItem), notified: make(map[int]bool), files: attachments.New(0)}
}

func (service *Native) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	if err := service.dock.ServiceStartup(ctx, options); err != nil {
		log.Printf("Dock badge unavailable: %v", err)
		service.dock = nil
	}
	if err := service.notifications.ServiceStartup(ctx, options); err != nil {
		log.Printf("Native notifications unavailable: %v", err)
		service.notifications = nil
		return nil
	}
	service.notifications.OnNotificationResponse(func(result notifications.NotificationResult) {
		if result.Error != nil || result.Response.ActionIdentifier != notifications.DefaultActionIdentifier {
			return
		}
		identifier, err := strconv.Atoi(result.Response.ID)
		if err != nil || identifier <= 0 {
			return
		}
		service.mutex.Lock()
		valid := service.notified[identifier] && service.states["main"].SignedIn
		service.mutex.Unlock()
		if !valid {
			return
		}
		if window, exists := service.app.Window.GetByName("main"); exists {
			window.Show().Restore()
			window.Focus()
			service.app.Event.Emit("native:open-message", identifier)
		}
	})
	return nil
}

func (service *Native) ServiceShutdown() error {
	if service.dock != nil {
		_ = service.dock.ServiceShutdown()
	}
	if service.notifications != nil {
		return service.notifications.ServiceShutdown()
	}
	return nil
}

func (service *Native) install(app *application.App) {
	service.app = app
	menu := application.NewMenu()
	appMenu := menu.AddSubmenu("REST Mail")
	appMenu.AddRole(application.About)
	appMenu.AddSeparator()
	service.settings = appMenu.Add("Settings…").SetAccelerator("CmdOrCtrl+,").SetEnabled(false).OnClick(func(*application.Context) { service.emitCommand("settings") })
	appMenu.AddSeparator()
	appMenu.AddRole(application.ServicesMenu)
	appMenu.AddRole(application.Hide)
	appMenu.AddRole(application.HideOthers)
	appMenu.AddRole(application.UnHide)
	appMenu.AddSeparator()
	appMenu.AddRole(application.Quit)
	groups := map[string]*application.Menu{}
	for _, group := range []string{"File", "Edit", "View", "Message"} {
		groups[group] = menu.AddSubmenu(group)
	}
	for _, role := range []application.Role{application.Undo, application.Redo, application.Cut, application.Copy, application.Paste, application.SelectAll} {
		groups["Edit"].AddRole(role)
	}
	for _, command := range native.Commands {
		command := command
		service.items[command.ID] = groups[command.Group].Add(command.Label).SetAccelerator(command.Accelerator).SetEnabled(command.ID == "close-window").OnClick(func(*application.Context) { _ = service.invokeMenu(command.ID) })
	}
	menu.AddRole(application.WindowMenu)
	menu.AddSubmenu("Help").Add("REST Mail Keyboard Shortcuts").OnClick(func(*application.Context) {
		app.Dialog.Info().SetTitle("REST Mail Keyboard Shortcuts").SetMessage("⌘N  New Message\n⌘R  Reply\n⇧⌘R  Reply All\n⇧⌘F  Forward\n⌘⌫  Delete\n⌃⌘A  Archive\n⇧⌘U  Mark Read / Unread\n⇧⌘L  Flag\n⇧⌘N  Refresh\n↑ / ↓  Select Message\nReturn  Open Message\n\nMessage shortcuts are inactive while typing.").Show()
	})
	app.Menu.Set(menu)
}

func (service *Native) emitCommand(action string) {
	window := service.app.Window.Current()
	if window != nil && window.IsFocused() {
		service.app.Event.Emit("native:command", map[string]any{"window": window.Name(), "action": action})
	}
}

func (service *Native) invokeMenu(identifier string) error {
	window := service.app.Window.Current()
	if window == nil || !window.IsFocused() {
		return errors.New("no focused REST Mail window")
	}
	service.mutex.Lock()
	state := service.states[window.Name()]
	service.mutex.Unlock()
	for _, command := range native.Commands {
		if command.ID != identifier {
			continue
		}
		if !state.Allows(command, window.Name() == "main") {
			return errors.New("menu item is disabled")
		}
		if identifier == "close-window" {
			if state.ComposeTab {
				service.emitCommand("close-tab")
			} else {
				window.Close()
			}
		} else {
			service.emitCommand(identifier)
		}
		return nil
	}
	return errors.New("unknown menu item")
}

func (service *Native) SyncState(name string, state native.State) {
	service.mutex.Lock()
	service.states[name] = state
	if name == "main" && !state.SignedIn {
		service.notified = make(map[int]bool)
		for draft := range service.compose {
			service.files.Clear(draft)
		}
		service.compose = make(map[string]*composeTab)
	}
	service.mutex.Unlock()
	service.updateMenu()
}

func (service *Native) updateMenu() {
	window := service.app.Window.Current()
	if window == nil {
		return
	}
	service.mutex.Lock()
	state := service.states[window.Name()]
	service.mutex.Unlock()
	for _, command := range native.Commands {
		service.items[command.ID].SetEnabled(state.Allows(command, window.Name() == "main")).SetLabel(state.Label(command))
	}
	service.settings.SetEnabled(state.SignedIn && !state.Editing)
}

func (service *Native) SyncCompose(name string, status ComposeStatus) {
	service.mutex.Lock()
	if service.closed[name] {
		service.mutex.Unlock()
		return
	}
	if service.compose[name] == nil {
		service.compose[name] = &composeTab{}
	}
	record := service.compose[name]
	record.Dirty, record.CanSave, record.Busy = status.Dirty, status.CanSave, status.Busy
	service.mutex.Unlock()
}

func (service *Native) RequestClose(name string) {
	service.mutex.Lock()
	record := service.compose[name]
	if record == nil || record.Prompting || record.Busy {
		service.mutex.Unlock()
		return
	}
	if !record.Dirty {
		service.mutex.Unlock()
		service.CompleteCompose(name, false)
		return
	}
	record.Prompting = true
	canSave := record.CanSave
	service.mutex.Unlock()
	service.app.Event.Emit("native:select-tab", name)
	window, exists := service.app.Window.GetByName("main")
	if !exists {
		return
	}
	clearPrompt := func(cancelQuit bool) {
		service.mutex.Lock()
		if record := service.compose[name]; record != nil {
			record.Prompting = false
		}
		if cancelQuit {
			service.quitting = false
		}
		service.mutex.Unlock()
	}
	dialog := service.app.Dialog.Question().SetTitle("Save this message as a draft?").SetMessage("Your unsaved changes will be lost if you don’t save them.").AttachToWindow(window)
	if canSave {
		dialog.AddButton("Save Draft").SetAsDefault().OnClick(func() { clearPrompt(false); service.app.Event.Emit("native:save-close", name) })
	} else {
		dialog.SetMessage("This message has recipients or attachments the server draft API cannot preserve. Return to the message to send it, or close without saving.")
	}
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { clearPrompt(true) })
	dialog.AddButton("Don’t Save").OnClick(func() { clearPrompt(false); service.CompleteCompose(name, false) })
	dialog.Show()
}

func (service *Native) CompleteCompose(name string, sent bool) {
	service.mutex.Lock()
	delete(service.compose, name)
	service.closed[name] = true
	service.files.Clear(name)
	quitting := service.quitting
	service.mutex.Unlock()
	service.app.Event.Emit("native:tab-closed", name)
	service.app.Event.Emit("native:mail-changed", map[string]bool{"sent": sent})
	if quitting {
		service.app.Quit()
	}
}

func (service *Native) CancelClose(name string) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.quitting = false
	if record := service.compose[name]; record != nil {
		record.Prompting = false
	}
}

type DropResult struct {
	Tab         string                 `json:"tab"`
	Attachments []attachments.Metadata `json:"attachments"`
}

func (service *Native) DropFiles(paths []string) (DropResult, error) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	state := service.states["main"]
	if !state.SignedIn {
		return DropResult{}, errors.New("sign in before attaching files")
	}
	name := state.ActiveTab
	if !state.ComposeTab || name == "" || name == "mail" || service.closed[name] {
		service.dropSequence++
		name = fmt.Sprintf("drop-%d", service.dropSequence)
	}
	files, err := service.files.Add(name, paths)
	if err != nil {
		return DropResult{}, err
	}
	if service.compose[name] == nil {
		service.compose[name] = &composeTab{}
	}
	service.compose[name].Dirty = true
	service.compose[name].CanSave = false
	result := DropResult{Tab: name, Attachments: files}
	service.app.Event.Emit("native:files-dropped", result)
	return result, nil
}

func (service *Native) RemoveAttachment(name, identifier string) []attachments.Metadata {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if !service.states["main"].SignedIn {
		return nil
	}
	return service.files.Remove(name, identifier)
}

func (service *Native) PickAttachments() (DropResult, error) {
	paths, err := service.app.Dialog.OpenFile().CanChooseFiles(true).CanChooseDirectories(false).SetTitle("Attach files").PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return DropResult{}, err
	}
	return service.DropFiles(paths)
}

func (service *Native) shouldQuit() bool {
	service.mutex.Lock()
	for name, record := range service.compose {
		if record.Busy || record.Prompting {
			service.mutex.Unlock()
			return false
		}
		if record.Dirty {
			service.quitting = true
			service.mutex.Unlock()
			go service.RequestClose(name)
			return false
		}
	}
	service.mutex.Unlock()
	return true
}

func (service *Native) SetUnread(unread int) error {
	if service.dock == nil {
		return nil
	}
	label := native.BadgeLabel(unread)
	if label == "" {
		return service.dock.RemoveBadge()
	}
	return service.dock.SetBadge(label)
}

func (service *Native) RequestNotificationPermission() (bool, error) {
	if service.notifications == nil {
		return false, nil
	}
	return service.notifications.RequestNotificationAuthorization()
}

func (service *Native) NewMail(identifier int, sender string, subject string) error {
	if identifier <= 0 || service.notifications == nil {
		return nil
	}
	service.mutex.Lock()
	state := service.states["main"]
	duplicate := service.notified[identifier]
	service.mutex.Unlock()
	window, exists := service.app.Window.GetByName("main")
	if !native.ShouldNotify(state, identifier, exists && window.IsFocused(), duplicate) {
		return nil
	}
	allowed, err := service.notifications.CheckNotificationAuthorization()
	if err != nil || !allowed {
		return err
	}
	if sender == "" {
		sender = "Unknown sender"
	}
	if subject == "" {
		subject = "(No subject)"
	}
	err = service.notifications.SendNotification(notifications.NotificationOptions{ID: strconv.Itoa(identifier), Title: sender, Body: subject})
	if err == nil {
		service.mutex.Lock()
		if len(service.notified) >= 512 {
			service.notified = make(map[int]bool)
		}
		service.notified[identifier] = true
		service.mutex.Unlock()
	}
	return err
}
