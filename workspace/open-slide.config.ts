import type { OpenSlideConfig } from '@open-slide/core';

// The MCP wrapper proxies this dev server. Host checks are handled by the
// wrapper, so accept any Host header here.
const openSlideConfig: OpenSlideConfig = {
  allowedHosts: true,
  build: { showSlideBrowser: false },
};

export default openSlideConfig;
