import { createRoot } from "react-dom/client";
import { App } from "./app";
import "@mantine/core/styles.css";
import "./styles.css";

createRoot(document.getElementById("app")!).render(<App />);
