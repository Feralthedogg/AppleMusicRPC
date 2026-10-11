// The Apple-signed system host can receive MediaRemote's artwork callbacks.
// No catalog IDs, account data, or listening history are read.
ObjC.import('Foundation');

function run() {
    const framework = $.NSBundle.bundleWithPath('/System/Library/PrivateFrameworks/MediaRemote.framework/');
    if (!framework || !framework.load) return 'null';
    const api = $.NSClassFromString('MRNowPlayingRequest');
    if (!api) return 'null';
    ObjC.bindFunction('objc_msgSend', ['void', ['id', 'SEL', 'id']]);

    function text(object) {
        try { return String(ObjC.unwrap(object.description) || ''); }
        catch (_) { return ''; }
    }
    function snapshot() {
        try {
            const path = api.localNowPlayingPlayerPath;
            if (String(ObjC.unwrap(path.client.bundleIdentifier)) !== 'com.apple.Music') return null;
            const info = api.localNowPlayingItem.nowPlayingInfo;
            return {
                path: path,
                title: text(info.objectForKey('kMRMediaRemoteNowPlayingInfoTitle')),
                artist: text(info.objectForKey('kMRMediaRemoteNowPlayingInfoArtist')),
                album: text(info.objectForKey('kMRMediaRemoteNowPlayingInfoAlbum'))
            };
        } catch (_) { return null; }
    }

    const before = snapshot();
    if (!before) return 'null';
    const request = api.alloc.initWithPlayerPath(before.path);
    let complete = false;
    let data = null;
    const callback = ObjC.block(['void', ['id', 'id']], function (artwork) {
        complete = true;
        try { data = artwork.imageData; } catch (_) {}
    });
    // Materialize a native block with Foundation before passing it to the private selector.
    const holder = $.NSMutableDictionary.alloc.init;
    holder.setObjectForKey(callback, 'completion');
    $.objc_msgSend(request, $.NSSelectorFromString('requestNowPlayingItemArtworkWithCompletion:'), holder.objectForKey('completion'));
    const deadline = Date.now() + 1500;
    while (!complete && Date.now() < deadline) {
        $.NSRunLoop.currentRunLoop.runUntilDate($.NSDate.dateWithTimeIntervalSinceNow(0.02));
    }
    if (!complete || !data) return 'null';
    const length = Number(data.length);
    if (!length || length > 8 * 1024 * 1024) return 'null';
    const after = snapshot();
    if (!after || before.title !== after.title || before.artist !== after.artist || before.album !== after.album) return 'null';
    return JSON.stringify({
        title: after.title,
        artist: after.artist,
        album: after.album,
        image_base64: ObjC.unwrap(data.base64EncodedStringWithOptions(0))
    });
}
