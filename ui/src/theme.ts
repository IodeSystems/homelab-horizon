import { createTheme } from "@mui/material/styles";

const theme = createTheme({
  palette: {
    mode: "dark",
    background: {
      default: "#1a1a2e",
      paper: "#16213e",
    },
    primary: {
      main: "#e94560",
      light: "#ff6b6b",
    },
    secondary: {
      main: "#0f3460",
    },
    success: {
      main: "#2ecc71",
    },
    error: {
      main: "#c0392b",
    },
    info: {
      main: "#2980b9",
    },
    text: {
      primary: "#eeeeee",
      secondary: "#888888",
    },
  },
  typography: {
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
  },
  components: {
    MuiCssBaseline: {
      styleOverrides: {
        body: {
          backgroundColor: "#1a1a2e",
        },
      },
    },
  },
});

export default theme;
