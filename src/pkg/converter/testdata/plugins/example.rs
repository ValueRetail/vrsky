// plugins/example.rs
// Example WASM plugin for VRSky converter
// Compile with: wasm-pack build --target bundler --release

use wasm_bindgen::prelude::*;

/// Calculate discount on base price
/// Example: calculate_discount(100.0, 15.0) = 85.0 (15% off)
#[wasm_bindgen]
pub fn calculate_discount(base_price: f64, percentage: f64) -> f64 {
    base_price * (1.0 - percentage / 100.0)
}

/// Apply tax rate to amount
/// Example: apply_tax(100.0, 0.08) = 108.0 (8% tax)
#[wasm_bindgen]
pub fn apply_tax(amount: f64, tax_rate: f64) -> f64 {
    amount * (1.0 + tax_rate)
}

/// Simple email validation
/// Example: validate_email("test@example.com") = true
#[wasm_bindgen]
pub fn validate_email(email: String) -> bool {
    email.contains('@') && email.contains('.')
}

/// Format currency amount with currency code
/// Example: format_currency(1234.56, "USD") = "$1,234.56"
#[wasm_bindgen]
pub fn format_currency(amount: f64, currency: String) -> String {
    match currency.as_str() {
        "USD" => format!("${:.2}", amount),
        "EUR" => format!("€{:.2}", amount),
        "GBP" => format!("£{:.2}", amount),
        _ => format!("{:.2}", amount),
    }
}

/// Calculate compound interest
/// Example: compound_interest(1000.0, 5.0, 10) = 1628.89
#[wasm_bindgen]
pub fn compound_interest(principal: f64, annual_rate: f64, years: i32) -> f64 {
    principal * (1.0 + annual_rate / 100.0).powi(years)
}

/// Add two numbers (simple test)
#[wasm_bindgen]
pub fn add(a: i32, b: i32) -> i32 {
    a + b
}

/// String concatenation
#[wasm_bindgen]
pub fn concat_strings(a: String, b: String) -> String {
    format!("{}{}", a, b)
}

/// Return boolean (true)
#[wasm_bindgen]
pub fn is_valid() -> bool {
    true
}

/// Fibonacci (for timeout testing - can be slow with large n)
#[wasm_bindgen]
pub fn fibonacci(n: i32) -> i32 {
    if n <= 1 {
        n
    } else {
        fibonacci(n - 1) + fibonacci(n - 2)
    }
}

/// Multiply two numbers
#[wasm_bindgen]
pub fn multiply(a: i32, b: i32) -> i32 {
    a * b
}

/// Check if string contains substring
#[wasm_bindgen]
pub fn contains_substring(text: String, substring: String) -> bool {
    text.contains(&substring)
}
