/** @type {import('next').NextConfig} */
const nextConfig = {
  // The console is fully behind a server-side session: nothing is statically
  // prerenderable, and every page must evaluate configuration at request time.
  output: 'standalone',
  reactStrictMode: true,
  poweredByHeader: false,
};

export default nextConfig;
