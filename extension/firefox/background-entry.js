// Chromium MV3 entry point. Keep the large worker implementation and the
// restart-only observation continuation as separate classic worker scripts.
importScripts('background.js', 'browser-recovery.js');
