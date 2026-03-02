/**
 * Footer Component
 * Application footer
 */

export default function Footer() {
  const currentYear = new Date().getFullYear()

  return (
    <footer className="bg-white border-t border-gray-200">
      <div className="container mx-auto px-6 py-4 text-center text-sm text-gray-600">
        <p>&copy; {currentYear} VRSky Integration Platform. All rights reserved.</p>
      </div>
    </footer>
  )
}
