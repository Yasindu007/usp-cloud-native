import type { Config } from "tailwindcss";

export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        brand: {
          100: "#ffefc2",
          300: "#ffca4a",
          400: "#ffb21f",
          500: "#f79209",
          700: "#b64708"
        },
        ocean: {
          500: "#0f766e",
          600: "#115e59"
        }
      },
      fontFamily: {
        sans: ["Manrope", "ui-sans-serif", "system-ui", "sans-serif"],
        display: ["Sora", "ui-sans-serif", "system-ui", "sans-serif"]
      },
      boxShadow: {
        panel: "0 20px 60px rgba(15, 23, 42, 0.18)"
      },
      keyframes: {
        rise: {
          "0%": { opacity: "0", transform: "translateY(12px)" },
          "100%": { opacity: "1", transform: "translateY(0)" }
        }
      },
      animation: {
        rise: "rise 0.45s ease-out both"
      }
    }
  },
  plugins: []
} satisfies Config;
