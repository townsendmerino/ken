use std::f64::consts::PI;

/// A circle with a radius.
#[derive(Debug, Clone)]
pub struct Circle {
    pub radius: f64,
}

impl Circle {
    pub fn new(radius: f64) -> Self {
        Circle { radius }
    }

    #[inline]
    pub fn area(&self) -> f64 {
        PI * self.radius * self.radius
    }
}

pub trait Shape {
    fn describe(&self) -> String;
}

#[cfg(test)]
mod tests {
    #[test]
    fn area_works() {
        assert!(true);
    }
}
