use objc2::{
    DefinedClass, MainThreadMarker, MainThreadOnly, define_class, msg_send,
    rc::{Retained, autoreleasepool},
    sel,
};
use objc2_app_kit::{
    NSApplication, NSApplicationActivationPolicy, NSEventMask, NSMenu, NSMenuItem, NSStatusBar,
    NSStatusItem,
};
use objc2_foundation::{NSDate, NSDefaultRunLoopMode, NSObject, NSObjectProtocol, NSString};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};

pub struct Actions {
    pub stop: Arc<AtomicBool>,
    pub retry: Arc<AtomicBool>,
}

define_class!(
    // NSObject imposes no extra subclass invariants; methods use the main thread only.
    #[unsafe(super = NSObject)]
    #[thread_kind = MainThreadOnly]
    #[ivars = Actions]
    struct MenuActions;

    unsafe impl NSObjectProtocol for MenuActions {}

    impl MenuActions {
        #[unsafe(method(quit:))]
        fn quit(&self, _sender: &NSObject) { self.ivars().stop.store(true, Ordering::Relaxed); }

        #[unsafe(method(retryArtwork:))]
        fn retry_artwork(&self, _sender: &NSObject) { self.ivars().retry.store(true, Ordering::Relaxed); }
    }
);

pub struct Menu {
    mtm: MainThreadMarker,
    app: Retained<NSApplication>,
    status_item: Retained<NSStatusItem>,
    _actions: Retained<MenuActions>,
    track: Retained<NSMenuItem>,
    discord: Retained<NSMenuItem>,
    artwork: Retained<NSMenuItem>,
}

fn item(mtm: MainThreadMarker, title: &str) -> Retained<NSMenuItem> {
    let item = NSMenuItem::new(mtm);
    item.setTitle(&NSString::from_str(title));
    item
}

impl Menu {
    pub fn new(mtm: MainThreadMarker, stop: Arc<AtomicBool>, retry: Arc<AtomicBool>) -> Self {
        let app = NSApplication::sharedApplication(mtm);
        app.setActivationPolicy(NSApplicationActivationPolicy::Accessory);
        let actions = MenuActions::alloc(mtm).set_ivars(Actions { stop, retry });
        // SAFETY: NSObject's init returns an initialized MenuActions instance.
        let actions: Retained<MenuActions> = unsafe { msg_send![super(actions), init] };
        let status_item = NSStatusBar::systemStatusBar().statusItemWithLength(-1.0);
        if let Some(button) = status_item.button(mtm) {
            button.setTitle(&NSString::from_str("AMRPC"));
            button.setToolTip(Some(&NSString::from_str("Apple Music → Discord")));
        }
        let menu = NSMenu::new(mtm);
        menu.setAutoenablesItems(false);
        let version = item(
            mtm,
            concat!("AppleMusicRPC ", env!("CARGO_PKG_VERSION"), " · Rust"),
        );
        version.setEnabled(false);
        menu.addItem(&version);
        let track = item(mtm, "Waiting for Music…");
        track.setEnabled(false);
        menu.addItem(&track);
        let discord = item(mtm, "Discord: waiting");
        discord.setEnabled(false);
        menu.addItem(&discord);
        let artwork = item(mtm, "Album cover: waiting");
        artwork.setEnabled(false);
        menu.addItem(&artwork);
        menu.addItem(&NSMenuItem::separatorItem(mtm));
        let retry_item = item(mtm, "Retry album cover");
        // SAFETY: The retained target implements both selectors with the sender argument.
        unsafe {
            retry_item.setTarget(Some(&actions));
            retry_item.setAction(Some(sel!(retryArtwork:)));
        }
        menu.addItem(&retry_item);
        let quit = item(mtm, "Quit");
        unsafe {
            quit.setTarget(Some(&actions));
            quit.setAction(Some(sel!(quit:)));
        }
        menu.addItem(&quit);
        status_item.setMenu(Some(&menu));
        app.finishLaunching();
        Self {
            mtm,
            app,
            status_item,
            _actions: actions,
            track,
            discord,
            artwork,
        }
    }

    pub fn update(&self, track: &str, discord: &str, artwork: &str) {
        self.track.setTitle(&NSString::from_str(track));
        self.discord.setTitle(&NSString::from_str(discord));
        self.artwork.setTitle(&NSString::from_str(artwork));
        if let Some(button) = self.status_item.button(self.mtm) {
            button.setToolTip(Some(&NSString::from_str(track)));
        }
    }

    pub fn pump(&self) {
        autoreleasepool(|_| {
            // Only the main thread creates/dequeues AppKit events.
            let date = NSDate::dateWithTimeIntervalSinceNow(0.05);
            let event = self.app.nextEventMatchingMask_untilDate_inMode_dequeue(
                NSEventMask::Any,
                Some(&date),
                unsafe { NSDefaultRunLoopMode },
                true,
            );
            if let Some(event) = event {
                self.app.sendEvent(&event);
            }
        });
    }
}

impl Drop for Menu {
    fn drop(&mut self) {
        NSStatusBar::systemStatusBar().removeStatusItem(&self.status_item);
    }
}
