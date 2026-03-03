# VRSky Design System Reference

## 🎨 Quick Color Reference

### Primary (Blue) - Main Actions & Links
```
primary-50:   #f0f9ff (Lightest)
primary-500:  #0ea5e9 (Primary)
primary-600:  #0284c7 (Hover)
primary-900:  #0c3d66 (Darkest)
```

### Secondary (Purple) - Accents
```
secondary-500: #a855f7 (Primary)
secondary-700: #7e22ce (Hover)
```

### Success (Green) - Running/OK Status
```
success-500: #22c55e (Primary)
success-600: #16a34a (Hover)
```

### Warning (Amber) - Stopped/Caution Status
```
warning-500: #f59e0b (Primary)
warning-600: #d97706 (Hover)
```

### Danger (Red) - Error Status
```
danger-500: #ef4444 (Primary)
danger-600: #dc2626 (Hover)
```

### Neutral (Gray) - Text & Backgrounds
```
neutral-50:   #f9fafb (Lightest background)
neutral-500:  #6b7280 (Mid text)
neutral-900:  #111827 (Darkest text)
```

---

## 🔘 Button Styles

### Primary Button
```tsx
<button className="btn-primary">
  Save Changes
</button>
```
Usage: Main actions, forms, CTAs

### Secondary Button
```tsx
<button className="btn-secondary">
  Cancel
</button>
```
Usage: Less important actions, defaults

### Outline Button
```tsx
<button className="btn-outline">
  Learn More
</button>
```
Usage: Links, alternative actions

### Danger Button
```tsx
<button className="btn-danger">
  Delete
</button>
```
Usage: Destructive actions

### Button Sizes
```tsx
<button className="btn-primary btn-sm">Small</button>
<button className="btn-primary">Default</button>
<button className="btn-primary btn-lg">Large</button>
```

---

## 📦 Card Styles

### Standard Card
```tsx
<div className="card p-6">
  <h3>Card Title</h3>
  <p>Card content...</p>
</div>
```

### Elevated Card
```tsx
<div className="card-elevated p-6">
  <h3>Important Content</h3>
</div>
```

### Card with Accent
```tsx
<div className="card-accent p-6">
  <h3>Highlighted Content</h3>
</div>
```

---

## 📝 Input Styles

### Text Input
```tsx
<input type="text" className="input-base" placeholder="Enter name..." />
```

### Input with Label
```tsx
<label className="label">Name</label>
<input type="text" className="input-base" />
```

### Input States
```tsx
<input className="input-base input-success" /> {/* Green border */}
<input className="input-base input-error" />   {/* Red border */}
```

---

## 🏷️ Badge Styles

### Status Badges
```tsx
<span className="badge badge-success">Running</span>
<span className="badge badge-warning">Stopped</span>
<span className="badge badge-danger">Error</span>
<span className="badge badge-primary">New</span>
```

---

## ⚠️ Alert Styles

### Alert Info
```tsx
<div className="alert alert-info">
  Information message here
</div>
```

### Alert Success
```tsx
<div className="alert alert-success">
  Success! Operation completed.
</div>
```

### Alert Warning
```tsx
<div className="alert alert-warning">
  Warning: Please check this.
</div>
```

### Alert Danger
```tsx
<div className="alert alert-danger">
  Error: Something went wrong.
</div>
```

---

## 🎬 Animations

### Fade In
```tsx
<div className="animate-fade-in">
  Content fades in on load
</div>
```

### Slide In (Up)
```tsx
<div className="animate-slide-in-up">
  Content slides up on load
</div>
```

### Slide In (Down)
```tsx
<div className="animate-slide-in-down">
  Content slides down on load
</div>
```

### Pulse
```tsx
<div className="animate-pulse-gentle">
  Content pulses gently
</div>
```

---

## 📐 Spacing Scale

```
xs  = 0.5rem  (8px)
sm  = 1rem    (16px)
md  = 1.5rem  (24px)
lg  = 2rem    (32px)
xl  = 3rem    (48px)
2xl = 4rem    (64px)
```

**Usage**:
```tsx
<div className="p-md">      {/* 24px padding */}
<div className="mt-lg">     {/* 32px margin-top */}
<div className="gap-sm">    {/* 16px gap in flex/grid */}
```

---

## 🎯 Typography

### Heading Sizes
```tsx
<h1 className="text-5xl font-bold">Page Title</h1>
<h2 className="text-4xl font-bold">Section Title</h2>
<h3 className="text-2xl font-semibold">Subsection</h3>
<h4 className="text-xl font-semibold">Small Heading</h4>
```

### Text Sizes
```tsx
<p className="text-base">Regular paragraph text</p>
<span className="text-sm">Small helper text</span>
<span className="text-xs">Tiny label text</span>
```

### Font Weights
```tsx
font-light     {/* 300 */}
font-normal    {/* 400 */}
font-medium    {/* 500 */}
font-semibold  {/* 600 */}
font-bold      {/* 700 */}
font-extrabold {/* 800 */}
```

---

## 🌙 Dark Mode

All components automatically support dark mode:

```tsx
// Background adapts to dark mode
<div className="bg-neutral-50 dark:bg-neutral-900">
  Content here
</div>

// Text color adapts
<p className="text-neutral-900 dark:text-neutral-50">
  Text adapts to dark mode
</p>

// Cards adapt
<div className="card"> {/* Works in both light and dark */}
  Content
</div>
```

---

## 📱 Responsive Classes

```tsx
<div className="
  grid 
  grid-cols-1        {/* Mobile: 1 column */}
  sm:grid-cols-2     {/* Tablet: 2 columns */}
  md:grid-cols-3     {/* Desktop: 3 columns */}
  lg:grid-cols-4     {/* Large screen: 4 columns */}
">
  {/* Responsive grid */}
</div>
```

---

## ✨ Common Patterns

### Card with Header and Content
```tsx
<div className="card-elevated">
  <div className="flex items-center justify-between mb-4">
    <h2 className="text-2xl font-bold">Title</h2>
    <a href="#" className="text-primary-600 hover:text-primary-700">
      View All →
    </a>
  </div>
  <div className="space-y-3">
    {/* Content */}
  </div>
</div>
```

### Stats Grid
```tsx
<div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
  <div className="card-elevated p-6">
    <div className="text-sm font-semibold text-neutral-600">Metric</div>
    <div className="text-3xl font-bold text-neutral-900 mt-2">42</div>
  </div>
  {/* Repeat for other stats */}
</div>
```

### Interactive List
```tsx
<div className="space-y-3">
  {items.map(item => (
    <a 
      key={item.id}
      href={`/items/${item.id}`}
      className="group flex items-center justify-between p-4 
                 bg-neutral-50 hover:bg-neutral-100 
                 rounded-lg transition-colors"
    >
      <div>
        <h3 className="font-semibold">{item.name}</h3>
        <p className="text-sm text-neutral-600">{item.description}</p>
      </div>
      <span className={`badge badge-${item.status}`}>
        {item.status}
      </span>
    </a>
  ))}
</div>
```

---

## 🎬 Transition Speeds

```
fast   = 150ms  (Quick response)
base   = 200ms  (Normal/default)
slow   = 300ms  (Slower for complex animations)
```

**Usage**:
```tsx
<div className="transition-all duration-base hover:shadow-lg">
  Smooth transition over 200ms
</div>
```

---

## 🔍 Accessibility Guidelines

1. **Color Contrast**: All text meets WCAG AA standard
2. **Focus States**: All interactive elements have visible focus rings
3. **Keyboard Navigation**: All components support tab navigation
4. **ARIA Labels**: Use `aria-label` for icon-only buttons
5. **Semantic HTML**: Use proper heading hierarchy (h1 → h2 → h3)

---

## 💡 Design Tips

1. **Use Semantic Colors**: success/warning/danger for status (not blue/red)
2. **Consistent Spacing**: Use spacing scale (sm, md, lg) not arbitrary values
3. **Hover States**: Ensure all buttons/links have hover feedback
4. **Dark Mode**: Test components in both light and dark modes
5. **Responsive**: Design mobile-first, then add complexity at larger screens

---

**For more info, see `tailwind.config.js` and `src/index.css`**
