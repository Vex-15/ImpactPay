import type { NextConfig } from "next";

const config: NextConfig = {
  reactStrictMode: true,
  // The API sets the session cookie itself; the browser talks to it directly via
  // NEXT_PUBLIC_API_URL, so no rewrites or proxies are needed.
  poweredByHeader: false,
};


export default config;
