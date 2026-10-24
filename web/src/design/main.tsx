// The design page, served at /design/.
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../styles/global';
import { DesignApp } from './DesignApp';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <DesignApp />
  </StrictMode>,
);
